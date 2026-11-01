"use client";
import { useEffect, useState } from "react";
import Link from "next/link";
import { PublicLayout } from "@/components/layout/PublicLayout";
import { AsciiFlow } from "@/components/landing/AsciiFlow";
import { SampleWorkspaces } from "@/components/landing/SampleWorkspaces";
import { PlatformPreview } from "@/components/landing/PlatformPreview";
import { Icon } from "@/components/ui/Icon";

export function IndexPage() {
  const [origin, setOrigin] = useState("http://127.0.0.1:8080");
  const [copied, setCopied] = useState(false),
    [copyError, setCopyError] = useState(false);
  useEffect(() => {
    setOrigin(window.location.origin);
  }, []);
  const command = `curl '${origin}/api/v1/health'`;
  return (
    <PublicLayout brandFooter>
      <div className="landing-page">
        <section className="landing-hero landing-hero-field">
          <AsciiFlow />
          <div className="hero-content">
            <div className="hero-copy">
              <h1>Run your ML work in one place.</h1>
              <p>
                Workspaces, pipelines, model serving and agents on
                infrastructure you control.
              </p>
              <div className="button-row">
                <a className="button primary" href="#workspaces">
                  Try a workspace
                </a>
                <Link className="button" href="/console">
                  Open the console
                </Link>
              </div>
              <div className="hero-command">
                <code
                  tabIndex={0}
                  role="region"
                  aria-label="Gateway health command"
                >
                  {command}
                </code>
                <button
                  type="button"
                  aria-label={
                    copied ? "Health command copied" : "Copy health command"
                  }
                  onClick={async () => {
                    try {
                      await navigator.clipboard.writeText(command);
                      setCopied(true);
                      setCopyError(false);
                    } catch {
                      setCopyError(true);
                    }
                  }}
                >
                  <Icon name={copied ? "check" : "copy"} />
                  {copied ? "Copied" : "Copy"}
                </button>
              </div>
              <div className="hero-links">
                <Link href="/api-docs">Explore the API</Link>
                {copyError && (
                  <span role="status">Select the command to copy it.</span>
                )}
              </div>
            </div>
          </div>
          <div className="hero-field-labels" aria-hidden="true">
            <span>Project files</span>
            <span>Runs and versions</span>
          </div>
        </section>
        <div className="landing-content">
          <PlatformPreview />
          <div className="ascii-divider" aria-hidden="true">
            <span>{". : : = + * # % @ @ % # * + = : : . ".repeat(12)}</span>
          </div>
          <SampleWorkspaces />
          <section className="landing-section access-section" id="security">
            <div className="access-intro">
              <Icon name="lock" />
              <h2>
                Your infrastructure.
                <br />
                Your access rules.
              </h2>
              <p>
                Grant services, projects and compute through platform policies.
              </p>
              <Link className="button" href="/console/profile">
                Inspect your access
              </Link>
            </div>
            <ol className="access-ledger">
              <li>
                <Icon name="projects" />
                <div>
                  <h3>Provision explicitly</h3>
                  <p>
                    Choose the services, projects, storage and compute an
                    account can use.
                  </p>
                </div>
              </li>
              <li>
                <Icon name="profile" />
                <div>
                  <h3>Inspect effective access</h3>
                  <p>
                    See roles, grants, quotas and the status of access requests.
                  </p>
                </div>
              </li>
              <li>
                <Icon name="logs" />
                <div>
                  <h3>Review changes</h3>
                  <p>Follow attributed approval actions in the audit trail.</p>
                </div>
              </li>
            </ol>
          </section>
        </div>
      </div>
    </PublicLayout>
  );
}
