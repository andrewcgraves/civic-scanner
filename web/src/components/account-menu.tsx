"use client";

import { usePathname, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useId, useRef, useState } from "react";

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
  const signOutRef = useRef<HTMLButtonElement>(null);
  const menuId = useId();

  useEffect(() => {
    if (!open) {
      return;
    }
    signOutRef.current?.focus();
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
      onBlur={(event) => {
        if (!containerRef.current?.contains(event.relatedTarget)) {
          setOpen(false);
        }
      }}
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
          <button
            ref={signOutRef}
            type="submit"
            className="whitespace-nowrap underline"
          >
            Sign out
          </button>
        </form>
      </div>
    </div>
  );
}

function signInHref(returnTo: string): string {
  return `/auth/sign-in?returnTo=${encodeURIComponent(returnTo)}`;
}

function SignInLinkWithQuery() {
  const pathname = usePathname();
  const query = useSearchParams().toString();
  return (
    <a
      href={signInHref(query === "" ? pathname : `${pathname}?${query}`)}
      className="underline"
    >
      Sign in
    </a>
  );
}

function SignInLink() {
  const pathname = usePathname();
  return (
    <Suspense
      fallback={
        <a href={signInHref(pathname)} className="underline">
          Sign in
        </a>
      }
    >
      <SignInLinkWithQuery />
    </Suspense>
  );
}

export function AccountMenu() {
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
    return <SignInLink />;
  }
  return <SignedInMenu email={state.email} />;
}
