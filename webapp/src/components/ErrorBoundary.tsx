import { Component } from "react";
import type { ErrorInfo, ReactNode } from "react";

/**
 * The last line of defence for the authenticated app.
 *
 * React unmounts the whole tree when a render throws and nothing catches it,
 * which turns any one component's bug into a blank page — a link is enough to
 * trigger it. This keeps the failure to a message the reader can act on.
 *
 * A class is not a style choice: `componentDidCatch` has no hook equivalent.
 */

interface ErrorBoundaryProps {
  /** Rendered in place of the subtree once it has thrown. */
  fallback: ReactNode;
  children: ReactNode;
}

interface ErrorBoundaryState {
  failed: boolean;
}

export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  override state: ErrorBoundaryState = { failed: false };

  static getDerivedStateFromError(): ErrorBoundaryState {
    return { failed: true };
  }

  override componentDidCatch(error: Error, info: ErrorInfo): void {
    // Nothing is shipped anywhere: this is the local console, so a developer
    // and a bug report both have the stack the fallback deliberately hides.
    console.error("The application stopped rendering:", error, info.componentStack);
  }

  override render(): ReactNode {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}
