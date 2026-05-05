import { NextResponse } from "next/server";
import { FILES_ENABLED } from "@/lib/features";

export const runtime = "nodejs";

export async function GET() {
  const spec = {
    openapi: "3.0.3",
    info: {
      title: "MesaHub Core API",
      version: "1.0.0",
      description:
        `MesaHub Core API for database execution${FILES_ENABLED ? " and file storage" : ""}. Versioned base path /api/v1 is supported.`,
    },
    servers: [
      { url: "/api", description: "Current API base" },
      { url: "/api/v1", description: "Versioned API base" },
    ],
    components: {
      securitySchemes: {
        bearerAuth: {
          type: "http",
          scheme: "bearer",
          bearerFormat: "JWT",
        },
      },
    },
    paths: {
      "/health": {
        get: {
          summary: "Health check",
          responses: { "200": { description: "OK" } },
        },
      },
      "/db": {
        get: { summary: "List databases", responses: { "200": { description: "OK" } } },
        post: { summary: "Create database", responses: { "201": { description: "Created" } } },
      },
      "/db/{name}/exec": {
        post: {
          summary: "Execute SQL (read/write)",
          security: [{ bearerAuth: [] }],
          parameters: [{ name: "name", in: "path", required: true, schema: { type: "string" } }],
          responses: { "200": { description: "Result" } },
        },
      },
      "/db/{name}/query": {
        post: {
          summary: "Execute read-only SQL",
          security: [{ bearerAuth: [] }],
          parameters: [{ name: "name", in: "path", required: true, schema: { type: "string" } }],
          responses: { "200": { description: "Result" } },
        },
      },
      ...(FILES_ENABLED ? {
        "/db/{name}/files": {
          get: {
            summary: "List files",
            security: [{ bearerAuth: [] }],
            parameters: [{ name: "name", in: "path", required: true, schema: { type: "string" } }],
            responses: { "200": { description: "OK" } },
          },
          post: {
            summary: "Upload file",
            security: [{ bearerAuth: [] }],
            parameters: [{ name: "name", in: "path", required: true, schema: { type: "string" } }],
            responses: { "201": { description: "Created" } },
          },
        },
        "/db/{name}/files/{id}": {
          get: {
            summary: "Download/View file",
            parameters: [
              { name: "name", in: "path", required: true, schema: { type: "string" } },
              { name: "id", in: "path", required: true, schema: { type: "string" } },
            ],
            responses: { "200": { description: "OK" } },
          },
          delete: {
            summary: "Delete file",
            security: [{ bearerAuth: [] }],
            parameters: [
              { name: "name", in: "path", required: true, schema: { type: "string" } },
              { name: "id", in: "path", required: true, schema: { type: "string" } },
            ],
            responses: { "204": { description: "Deleted" } },
          },
        },
        "/db/{name}/tokens/files": {
          post: {
            summary: "Create read-only file access token",
            security: [{ bearerAuth: [] }],
            parameters: [{ name: "name", in: "path", required: true, schema: { type: "string" } }],
            responses: { "200": { description: "OK" } },
          },
        },
        "/db/{name}/tokens/files/revoke": {
          post: {
            summary: "Revoke file access token",
            security: [{ bearerAuth: [] }],
            parameters: [{ name: "name", in: "path", required: true, schema: { type: "string" } }],
            responses: { "200": { description: "Revoked" } },
          },
        },
      } : {}),
      "/maintenance/cleanup": {
        post: {
          summary: "Cleanup expired files and token revocations",
          responses: { "200": { description: "Cleanup result" } },
        },
      },
      "/metrics": {
        get: {
          summary: "Operational metrics",
          responses: { "200": { description: "Metrics" } },
        },
      },
    },
  };

  return NextResponse.json(spec);
}
