import { TextDecoder, TextEncoder } from "node:util";
import * as testingLibraryMatchers from "@testing-library/jest-dom/matchers";
import { expect, vi } from "vitest";

import "@testing-library/jest-dom/vitest";
import failOnConsole from "vitest-fail-on-console";

expect.extend(testingLibraryMatchers);

// jsdom does not implement matchMedia; components using SidebarProvider require it
Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: () => ({
    matches: false,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }),
});

// Polyfill ResizeObserver for jsdom (required by Radix Checkbox)
global.ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

// Polyfill pointer capture APIs for jsdom (required by Radix Select and other pointer-based components)
if (!window.Element.prototype.hasPointerCapture) {
  window.Element.prototype.hasPointerCapture = () => false;
}
if (!window.Element.prototype.setPointerCapture) {
  window.Element.prototype.setPointerCapture = () => {};
}
if (!window.Element.prototype.releasePointerCapture) {
  window.Element.prototype.releasePointerCapture = () => {};
}
if (!window.Element.prototype.scrollIntoView) {
  window.Element.prototype.scrollIntoView = () => {};
}

// Polyfill TextEncoder/TextDecoder for jsdom environment
global.TextEncoder = TextEncoder;
// @ts-expect-error - TextDecoder types are compatible
global.TextDecoder = TextDecoder;

// Fail tests that log errors or warnings to console
failOnConsole({
  shouldFailOnDebug: false,
  shouldFailOnError: true,
  shouldFailOnInfo: false,
  shouldFailOnLog: false,
  shouldFailOnWarn: true,
});

// Global mocks used across test files
vi.mock("next/headers", () => ({
  headers: vi.fn(() => Promise.resolve(new Headers())),
  cookies: vi.fn(() =>
    Promise.resolve({ get: vi.fn(), getAll: vi.fn(() => []) }),
  ),
}));

vi.mock("next/navigation", () => ({
  redirect: vi.fn(),
  notFound: vi.fn(() => {
    throw Object.assign(new Error("NEXT_NOT_FOUND"), {
      digest: "NEXT_NOT_FOUND",
    });
  }),
  useRouter: vi.fn(() => ({
    push: vi.fn(),
    replace: vi.fn(),
    refresh: vi.fn(),
    back: vi.fn(),
    forward: vi.fn(),
    prefetch: vi.fn(),
  })),
  useSearchParams: vi.fn(() => new URLSearchParams()),
}));

// Common UI/runtime mocks
vi.mock("next/image", () => ({
  default: () => null,
}));

vi.mock("sonner", () => ({
  Toaster: () => null,
  toast: {
    error: vi.fn(),
    success: vi.fn(),
    info: vi.fn(),
    warning: vi.fn(),
  },
}));

export const mockSetTheme = vi.fn();
vi.mock("next-themes", () => ({
  useTheme: () => ({
    theme: "system",
    setTheme: mockSetTheme,
  }),
  ThemeProvider: ({ children }: { children: React.ReactNode }) => children,
}));

import { cleanup } from "@testing-library/react";
// Reset mocks between test cases globally
import { afterEach } from "vitest";

afterEach(() => {
  // Clear calls/instances, but keep hoisted module mock implementations intact
  vi.clearAllMocks();
  // Restore any globals stubbed with vi.stubGlobal()
  vi.unstubAllGlobals();
  // Clean up DOM after each test
  cleanup();
});
