"use client";

import { usePathname } from "next/navigation";
import { useEffect, useId, useRef, useState } from "react";

import { signOutAction } from "@/lib/auth/actions";
import { parseSessionState, type SessionState } from "@/lib/auth/session-state";

async function loadSessionState(): Promise<SessionState | null> {
  try {
    const response = await fetch("/auth/session", {
      cache: "no-store",
      headers: { accept: "application/json" },
    });
    return response.ok ? parseSessionState(await response.json()) : null;
  } catch {
    return null;
  }
}

function SignedInMenu({ email }: { email: string | null }) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const menuId = useId();

  useEffect(() => {
    if (!open) {
      return;
    }
    function closeOnOutsidePointer(event: PointerEvent) {
      if (!containerRef.current?.contains(event.target as Node)) {
        setOpen(false);
      }
    }
    document.addEventListener("pointerdown", closeOnOutsidePointer);
    return () => {
      document.removeEventListener("pointerdown", closeOnOutsidePointer);
    };
  }, [open]);

  return (
    <div
      ref={containerRef}
      className="relative"
      onKeyDown={(event) => {
        if (event.key === "Escape" && open) {
          setOpen(false);
          buttonRef.current?.focus();
        }
      }}
    >
      <button
        ref={buttonRef}
        type="button"
        aria-expanded={open}
        aria-controls={menuId}
        onClick={() => {
          setOpen((wasOpen) => !wasOpen);
        }}
        className="underline"
      >
        {email ?? "Account"}
      </button>
      <div
        id={menuId}
        hidden={!open}
        className="absolute right-0 z-10 mt-2 rounded border border-gray-300 bg-white p-3 shadow"
      >
        <form action={signOutAction}>
          <button type="submit" className="whitespace-nowrap underline">
            Sign out
          </button>
        </form>
      </div>
    </div>
  );
}

export function AccountMenu() {
  const pathname = usePathname();
  const [state, setState] = useState<SessionState | null>(null);

  useEffect(() => {
    let current = true;
    void loadSessionState().then((loaded) => {
      if (current) {
        setState(loaded);
      }
    });
    return () => {
      current = false;
    };
  }, []);

  if (state === null) {
    return null;
  }
  if (!state.signedIn) {
    return (
      <a
        href={`/auth/sign-in?returnTo=${encodeURIComponent(pathname)}`}
        className="underline"
      >
        Sign in
      </a>
    );
  }
  return <SignedInMenu email={state.email} />;
}
