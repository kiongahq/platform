"use client";
import { useEffect } from "react";

/**
 * The install ships no landing page. The gateway answers "/" with a redirect
 * to the console (and sign-in middleware sends signed-out visitors to the
 * sign-in page); this page covers static hosting and `next dev`.
 */
export function RootRedirect() {
  useEffect(() => {
    window.location.replace("/console");
  }, []);
  return (
    <main className="login-page" id="main">
      <p role="status">
        Opening the console… <a href="/console">Continue</a>
      </p>
    </main>
  );
}
