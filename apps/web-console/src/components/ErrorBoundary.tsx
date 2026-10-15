import { Component, type ReactNode } from "react";

/**
 * Keeps a rendering error in one page from blanking the whole console: the menu stays usable and the page shows
 * what failed. Keyed by route in App, so moving to another page starts fresh.
 */
export class PageErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div className="card">
        <div className="error" role="alert">
          <strong>نمایش این صفحه با خطا روبه‌رو شد.</strong>
          <small dir="ltr">{this.state.error.message}</small>
        </div>
        <p className="muted small">صفحه را دوباره بارگذاری کنید؛ اگر تکرار شد، متن خطا را برای پشتیبانی بفرستید.</p>
        <button className="btn" onClick={() => location.reload()}>بارگذاری دوباره</button>
      </div>
    );
  }
}
