export interface ClientNavigator {
  userAgentData?: { platform?: string };
  userAgent: string;
}

// Client hints take precedence, but an empty hint must not hide Safari's UA.
export function isAppleClient(nav: ClientNavigator | undefined = globalThis.navigator): boolean {
  return /mac|iphone|ipad|ipod/i.test(nav?.userAgentData?.platform || nav?.userAgent || "");
}

export function isWindowsClient(nav: ClientNavigator | undefined = globalThis.navigator): boolean {
  return /windows/i.test(nav?.userAgentData?.platform || nav?.userAgent || "");
}
