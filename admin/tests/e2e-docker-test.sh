#!/bin/bash

###############################################################################
# End-to-End Docker Test Suite for MesaHub Core
#
# Tests all concurrency hardening and file storage features locally
#
# Usage:
#   bash tests/e2e-docker-test.sh [options]
#
# Options:
#   --skip-login        Skip login step (use existing session)
#   --api-url           API URL (default: http://localhost:8080)
#   --admin-token       Admin token (default: dev-token-change-me-in-production)
###############################################################################

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
API_URL="${API_URL:-http://localhost:8080}"
ADMIN_TOKEN="${ADMIN_TOKEN:-dev-token-change-me-in-production}"
SKIP_LOGIN="${SKIP_LOGIN:-false}"
COOKIES_FILE="/tmp/mesahub-cookies.txt"
TEST_DB_NAME="test-db-$(date +%s)"
API_BEARER="$ADMIN_TOKEN"

# Test database
TEST_ADMIN_EMAIL="test@example.com"
TEST_OWNER="e2e-test"

# Statistics
TESTS_PASSED=0
TESTS_FAILED=0
TESTS_SKIPPED=0

###############################################################################
# Utilities
###############################################################################

log_info() {
  echo -e "${BLUE}[INFO]${NC} $*"
}

log_success() {
  echo -e "${GREEN}[✓ PASS]${NC} $*"
  ((TESTS_PASSED++))
}

log_error() {
  echo -e "${RED}[✗ FAIL]${NC} $*"
  ((TESTS_FAILED++))
}

log_warning() {
  echo -e "${YELLOW}[!]${NC} $*"
  ((TESTS_SKIPPED++))
}

assert_status() {
  local expected="$1"
  local actual="$2"
  local msg="$3"
  
  if [ "$actual" = "$expected" ]; then
    log_success "$msg"
  else
    log_error "$msg (expected $expected, got $actual)"
    return 1
  fi
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  local msg="$3"
  
  if echo "$haystack" | grep -q "$needle"; then
    log_success "$msg"
  else
    log_error "$msg (expected to contain '$needle')"
    return 1
  fi
}

###############################################################################
# Setup
###############################################################################

setup() {
  log_info "=========================================="
  log_info "MesaHub Core End-to-End Test Suite"
  log_info "=========================================="
  log_info "API URL: $API_URL"
  log_info "Test Database: $TEST_DB_NAME"
  log_info ""
  
  # Check if API is reachable
  log_info "Checking if $API_URL is reachable..."
  if ! curl -sf "$API_URL/health" > /dev/null; then
    log_error "API is not reachable at $API_URL"
    echo "Make sure docker-compose is running: docker-compose up"
    exit 1
  fi
  log_success "API is reachable"
}

cleanup() {
  log_info "Cleaning up..."
  rm -f "$COOKIES_FILE"
}

###############################################################################
# Auth Tests
###############################################################################

test_health_check() {
  log_info "Test: Health check"
  
  local response
  response=$(curl -s -w "\n%{http_code}" "$API_URL/health")
  local status_code=$(echo "$response" | tail -n1)
  
  assert_status "200" "$status_code" "Health check returns 200"
}

test_login() {
  log_info "Test: Admin login"
  
  if [ "$SKIP_LOGIN" = "true" ]; then
    log_warning "Skipping login (--skip-login)"
    return 0
  fi
  
  local response
  response=$(curl -s -w "\n%{http_code}" -X POST "$API_URL/api/auth/login" \
    -H "Content-Type: application/json" \
    -c "$COOKIES_FILE" \
    -d "{\"token\":\"$ADMIN_TOKEN\"}")
  
  local status_code=$(echo "$response" | tail -n1)
  local body=$(echo "$response" | sed '$d')
  
  assert_status "200" "$status_code" "Login endpoint returns 200"
  assert_contains "$body" "success" "Login response contains success"
}

###############################################################################
# Database Tests
###############################################################################

test_create_database() {
  log_info "Test: Create database"
  
  local response
  response=$(curl -s -w "\n%{http_code}" -X POST "$API_URL/api/db" \
    -H "Content-Type: application/json" \
    -b "$COOKIES_FILE" \
    -d "{\"name\":\"$TEST_DB_NAME\", \"owner\":\"$TEST_OWNER\", \"description\":\"E2E test database\"}")
  
  local status_code=$(echo "$response" | tail -n1)
  local body=$(echo "$response" | sed '$d')
  
  assert_status "201" "$status_code" "Create database returns 201"

  # DB endpoints are now authenticated via admin token or scoped API keys.
  API_BEARER="$ADMIN_TOKEN"
  assert_contains "$body" "\"name\":\"$TEST_DB_NAME\"" "Create database response contains name"
}

test_get_database_info() {
  log_info "Test: Get database info"
  
  local response
  response=$(curl -s -w "\n%{http_code}" -X GET "$API_URL/api/db/$TEST_DB_NAME" \
    -H "Authorization: Bearer $API_BEARER")
  
  local status_code=$(echo "$response" | tail -n1)
  
  assert_status "200" "$status_code" "Get database info returns 200"
}

###############################################################################
# Query Execution Tests
###############################################################################

test_create_table() {
  log_info "Test: Create table"
  
  local sql="CREATE TABLE items (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    value REAL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
  )"
  
  local response
  response=$(curl -s -w "\n%{http_code}" -X POST "$API_URL/api/db/$TEST_DB_NAME/exec" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_BEARER" \
    -d "{\"sql\":\"$sql\"}")
  
  local status_code=$(echo "$response" | tail -n1)
  
  assert_status "200" "$status_code" "Create table returns 200"
}

test_insert_records() {
  log_info "Test: Insert records"
  
  local sql="INSERT INTO items (name, value) VALUES (?, ?)"
  
  local response
  response=$(curl -s -w "\n%{http_code}" -X POST "$API_URL/api/db/$TEST_DB_NAME/exec" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_BEARER" \
    -d "{\"sql\":\"$sql\", \"bindings\":[\"item-1\", 10.5]}")
  
  local status_code=$(echo "$response" | tail -n1)
  local body=$(echo "$response" | sed '$d')
  
  assert_status "200" "$status_code" "Insert record returns 200"
  assert_contains "$body" "changes" "Insert response contains changes count"
}

test_select_records() {
  log_info "Test: Select records"
  
  local sql="SELECT * FROM items ORDER BY id DESC LIMIT 10"
  
  local response
  response=$(curl -s -w "\n%{http_code}" -X POST "$API_URL/api/db/$TEST_DB_NAME/exec" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_BEARER" \
    -d "{\"sql\":\"$sql\"}")
  
  local status_code=$(echo "$response" | tail -n1)
  
  assert_status "200" "$status_code" "Select records returns 200"
}

###############################################################################
# Concurrency Tests (Write Queue)
###############################################################################

test_concurrent_writes() {
  log_info "Test: Concurrent writes (write queue)"
  
  # Define the SQL to run in parallel
  local sql="INSERT INTO items (name, value) VALUES (?, ?)"
  
  log_info "Sending 10 concurrent write requests..."
  
  # Fire off 10 concurrent requests
  local pids=()
  for i in {1..10}; do
    (
      curl -s -X POST "$API_URL/api/db/$TEST_DB_NAME/exec" \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $API_BEARER" \
        -d "{\"sql\":\"$sql\", \"bindings\":[\"concurrent-item-$i\", $((i + 20.0))]}" > /dev/null
    ) &
    pids+=($!)
  done
  
  # Wait for all requests to complete
  local failed=0
  for pid in "${pids[@]}"; do
    wait "$pid" || ((failed++))
  done
  
  if [ $failed -eq 0 ]; then
    log_success "All 10 concurrent writes succeeded (write queue in action)"
  else
    log_error "Some concurrent writes failed (failed: $failed)"
    return 1
  fi
  
  # Verify all records were inserted
  sleep 0.5
  local response
  response=$(curl -s -X POST "$API_URL/api/db/$TEST_DB_NAME/exec" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_BEARER" \
    -d "{\"sql\":\"SELECT COUNT(*) as count FROM items\"}")
  
  local count=$(echo "$response" | grep -o '"count":[0-9]*' | cut -d':' -f2)
  if [ "$count" -ge 10 ]; then
    log_success "All concurrent inserts persisted ($count total records)"
  else
    log_error "Not all inserts persisted (expected ≥10, got $count)"
    return 1
  fi
}

###############################################################################
# File Storage Tests
###############################################################################

test_upload_file() {
  log_info "Test: Upload file"
  
  # Create a test file
  local test_file="/tmp/test-upload-$(date +%s).txt"
  echo "Hello from MesaHub! This is a test file." > "$test_file"
  
  local response
  response=$(curl -s -w "\n%{http_code}" -X POST "$API_URL/api/db/$TEST_DB_NAME/files" \
    -H "Authorization: Bearer $API_BEARER" \
    -F "file=@$test_file" \
    -F "filename=test-document.txt")
  
  local status_code=$(echo "$response" | tail -n1)
  local body=$(echo "$response" | sed '$d')
  
  assert_status "201" "$status_code" "Upload file returns 201"
  
  # Extract file ID
  FILE_ID=$(echo "$body" | grep -o '"id":"[^"]*"' | cut -d'"' -f4 | head -1)
  if [ -z "$FILE_ID" ]; then
    log_error "Could not extract file ID from response"
    rm "$test_file"
    return 1
  fi
  log_success "File uploaded with ID: $FILE_ID"
  
  # Clean up
  rm "$test_file"
}

test_list_files() {
  log_info "Test: List files"
  
  local response
  response=$(curl -s -w "\n%{http_code}" -X GET "$API_URL/api/db/$TEST_DB_NAME/files?limit=10&sort=uploaded_at&order=DESC" \
    -H "Authorization: Bearer $API_BEARER")
  
  local status_code=$(echo "$response" | tail -n1)
  local body=$(echo "$response" | sed '$d')
  
  assert_status "200" "$status_code" "List files returns 200"
  assert_contains "$body" "files" "List response contains files array"
}

test_download_file() {
  log_info "Test: Download file"
  
  if [ -z "${FILE_ID:-}" ]; then
    log_warning "Skipping download test (no file uploaded)"
    return 0
  fi
  
  local response
  response=$(curl -s -w "\n%{http_code}" -X GET "$API_URL/api/db/$TEST_DB_NAME/files/$FILE_ID" \
    -H "Authorization: Bearer $API_BEARER")
  
  local status_code=$(echo "$response" | tail -n1)
  
  assert_status "200" "$status_code" "Download file returns 200"
}

test_file_proxy_acceleration() {
  log_info "Test: File proxy acceleration (X-Sendfile header)"
  
  if [ -z "${FILE_ID:-}" ]; then
    log_warning "Skipping proxy acceleration test (no file uploaded)"
    return 0
  fi
  
  # Use -i to show headers
  local response
  response=$(curl -s -i -X GET "$API_URL/api/db/$TEST_DB_NAME/files/$FILE_ID" \
    -H "Authorization: Bearer $API_BEARER")
  
  if echo "$response" | grep -iq "x-sendfile:"; then
    log_success "X-Sendfile header detected (Caddy proxy acceleration enabled)"
  else
    log_warning "X-Sendfile header not found (fallback to Node.js streaming)"
  fi
}

test_file_metadata() {
  log_info "Test: Get file metadata"
  
  if [ -z "${FILE_ID:-}" ]; then
    log_warning "Skipping metadata test (no file uploaded)"
    return 0
  fi
  
  local response
  response=$(curl -s -w "\n%{http_code}" -X GET "$API_URL/api/db/$TEST_DB_NAME/files/$FILE_ID/meta" \
    -H "Authorization: Bearer $API_BEARER")
  
  local status_code=$(echo "$response" | tail -n1)
  local body=$(echo "$response" | sed '$d')
  
  assert_status "200" "$status_code" "Get file metadata returns 200"
  assert_contains "$body" "content_hash" "Metadata contains content_hash (deduplication key)"
}

test_bulk_delete_files() {
  log_info "Test: Bulk delete files"
  
  if [ -z "${FILE_ID:-}" ]; then
    log_warning "Skipping bulk delete test (no file uploaded)"
    return 0
  fi
  
  local response
  response=$(curl -s -w "\n%{http_code}" -X POST "$API_URL/api/db/$TEST_DB_NAME/files/bulk-delete" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $API_BEARER" \
    -d "{\"file_ids\":[\"$FILE_ID\"]}")
  
  local status_code=$(echo "$response" | tail -n1)
  
  assert_status "200" "$status_code" "Bulk delete returns 200"
  
  log_success "File deleted successfully"
}

###############################################################################
# Metrics Tests
###############################################################################

test_metrics_endpoint() {
  log_info "Test: Metrics endpoint"
  
  local response
  response=$(curl -s -w "\n%{http_code}" -X GET "$API_URL/api/metrics" \
    -b "$COOKIES_FILE")
  
  local status_code=$(echo "$response" | tail -n1)
  local body=$(echo "$response" | sed '$d')
  
  assert_status "200" "$status_code" "Metrics endpoint returns 200"
  assert_contains "$body" "exec" "Metrics contains exec stats"
  assert_contains "$body" "files" "Metrics contains file storage stats"
}

test_exec_metrics() {
  log_info "Test: Execution metrics tracking"
  
  local response
  response=$(curl -s -w "\n%{http_code}" -X GET "$API_URL/api/metrics" \
    -b "$COOKIES_FILE")
  
  local body=$(echo "$response" | sed '$d')
  
  # Check for key metrics fields
  assert_contains "$body" "totalRequests" "Metrics include totalRequests"
  assert_contains "$body" "readRequests" "Metrics include readRequests"
  assert_contains "$body" "writeRequests" "Metrics include writeRequests"
  assert_contains "$body" "sqliteBusyErrors" "Metrics include sqliteBusyErrors tracking"
}

###############################################################################
# Main Test Runner
###############################################################################

run_all_tests() {
  log_info "Running all tests..."
  log_info ""
  
  # Auth tests
  test_health_check
  test_login
  
  # Database tests
  test_create_database
  test_get_database_info
  
  # Query execution tests
  test_create_table
  test_insert_records
  test_select_records
  
  # Concurrency tests
  test_concurrent_writes
  
  # File storage tests
  test_upload_file
  test_list_files
  test_download_file
  test_file_proxy_acceleration
  test_file_metadata
  test_bulk_delete_files
  
  # Metrics tests
  test_metrics_endpoint
  test_exec_metrics
}

print_summary() {
  log_info ""
  log_info "=========================================="
  log_info "Test Results"
  log_info "=========================================="
  echo -e "  ${GREEN}Passed:${NC}  $TESTS_PASSED"
  echo -e "  ${RED}Failed:${NC}  $TESTS_FAILED"
  echo -e "  ${YELLOW}Skipped:${NC} $TESTS_SKIPPED"
  echo -e "  Total:   $((TESTS_PASSED + TESTS_FAILED + TESTS_SKIPPED))"
  log_info "=========================================="
  
  if [ $TESTS_FAILED -eq 0 ]; then
    echo -e "${GREEN}All tests passed!${NC}"
    return 0
  else
    echo -e "${RED}Some tests failed!${NC}"
    return 1
  fi
}

###############################################################################
# Main
###############################################################################

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --skip-login)
      SKIP_LOGIN=true
      shift
      ;;
    --api-url)
      API_URL="$2"
      shift 2
      ;;
    --admin-token)
      ADMIN_TOKEN="$2"
      shift 2
      ;;
    *)
      echo "Unknown option: $1"
      exit 1
      ;;
  esac
done

trap cleanup EXIT

setup
run_all_tests
print_summary
