import { useStudioContext } from "@/context/driver-provider";
import { cn } from "@/lib/utils";
import { ArrowLeft, BugBeetle } from "@phosphor-icons/react";
import { useTheme } from "next-themes";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { ReactElement, useState } from "react";
import ThemeToggle from "../theme-toggle";
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuSeparator,
    DropdownMenuTrigger,
} from "../ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";

export interface SidebarTabItem {
  key: string;
  icon: ReactElement;
  name: string;
  content?: ReactElement;
  onClick?: () => void;
}

interface SidebarTabProps {
  tabs: SidebarTabItem[];
}

export default function SidebarTab({ tabs }: Readonly<SidebarTabProps>) {
  const { forcedTheme } = useTheme();
  const [selectedIndex, setSelectedIndex] = useState(0);
  const [loadedIndex, setLoadedIndex] = useState(() => {
    const a: boolean[] = new Array(tabs.length).fill(false);
    a[0] = true;
    return a;
  });

  const searchParams = useSearchParams();
  const disableToggle =
    searchParams.get("disableThemeToggle") === "1" || forcedTheme;

  const config = useStudioContext();

  return (
    <div className={cn("flex h-full bg-neutral-50 dark:bg-neutral-950")}>
      <div className={cn("shrink-0")}>
        <div className="flex h-full flex-col gap-4 border-r border-neutral-200 p-3 dark:border-neutral-800">
          <DropdownMenu modal={false}>
            <DropdownMenuTrigger>
              <div className="mb-2 ml-1 flex h-8 w-8 items-center justify-center rounded text-neutral-400 hover:text-white transition-colors">
                <span className="text-xs font-bold tracking-tight select-none">db</span>
              </div>
            </DropdownMenuTrigger>
            <DropdownMenuContent side="right" align="start">
              <div className="px-3 py-2">
                <div className="font-semibold text-sm">MesaHub</div>
                <div className="text-xs text-neutral-500">SQLite viewer</div>
              </div>

              {!disableToggle && (
                <div className="flex p-2">
                  <ThemeToggle />
                </div>
              )}

              {config.onBack && (
                <DropdownMenuItem onClick={config.onBack}>
                  <ArrowLeft className="mr-2" />
                  Back to bases
                </DropdownMenuItem>
              )}
              {config.onBack && <DropdownMenuSeparator />}
              <DropdownMenuItem>
                <BugBeetle className="mr-2" />
                <Link
                  className="block w-full"
                  href="https://github.com/mesahub-db/mesahub-core/issues"
                  target="_blank"
                >
                  Report issues
                </Link>
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>

          {tabs.map(({ key, name, icon, onClick }, idx) => {
            return (
              <Tooltip key={key}>
                <TooltipTrigger asChild>
                  <button
                    onClick={() => {
                      if (onClick) {
                        onClick();
                        return;
                      }

                      if (!loadedIndex[idx]) {
                        loadedIndex[idx] = true;
                        setLoadedIndex([...loadedIndex]);
                      }

                      if (idx !== selectedIndex) {
                        setSelectedIndex(idx);
                      }
                    }}
                    className={cn(
                      "cursor flex h-10 w-10 cursor-pointer flex-col items-center justify-center gap-0.5 text-neutral-400 hover:text-neutral-900 dark:text-neutral-600 dark:hover:text-neutral-100",
                      selectedIndex === idx
                        ? "rounded-xl bg-neutral-200 text-neutral-900 dark:bg-neutral-800 dark:text-neutral-100"
                        : undefined
                    )}
                  >
                    {icon}
                  </button>
                </TooltipTrigger>
                <TooltipContent side="right">{name}</TooltipContent>
              </Tooltip>
            );
          })}
        </div>
      </div>

      <div className="relative flex h-full grow overflow-hidden">
        {tabs
          .filter((tab) => tab.content)
          .map((tab, tabIndex) => {
            const selected = selectedIndex === tabIndex;

            return (
              <div
                key={tab.key}
                style={{
                  contentVisibility: selected ? "auto" : "hidden",
                  zIndex: selected ? 0 : -1,
                  position: "absolute",
                  display: "flex",
                  left: 0,
                  right: 0,
                  bottom: 0,
                  top: 0,
                }}
              >
                {loadedIndex[tabIndex] && tab.content}
              </div>
            );
          })}
      </div>
    </div>
  );
}
