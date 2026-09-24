// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import { act, cleanup, renderHook } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { joinTranscript, useVoiceInput } from "./use-voice-input";

afterEach(() => {
  cleanup();
  Reflect.deleteProperty(window, "SpeechRecognition");
  Reflect.deleteProperty(window, "webkitSpeechRecognition");
});

describe("voice input", () => {
  it("appends a transcript to existing input", () => {
    expect(joinTranscript("Explain this", "in simple terms")).toBe("Explain this in simple terms");
  });

  it("does not add leading space to an empty prompt", () => {
    expect(joinTranscript("", " hello world")).toBe("hello world");
  });

  it("keeps the typed draft after denied speech recognition", () => {
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    class DeniedRecognition {
      start() {
        throw new DOMException("Permission denied", "NotAllowedError");
      }
      stop() {}
    }
    Object.defineProperty(window, "SpeechRecognition", {
      configurable: true,
      value: DeniedRecognition,
    });
    const { result } = renderHook(() => {
      const [draft, setDraft] = useState("Typed draft");
      return { draft, voice: useVoiceInput(draft, setDraft) };
    });
    act(() => result.current.voice.toggle());
    expect(result.current.draft).toBe("Typed draft");
    expect(result.current.voice.isListening).toBe(false);
    expect(result.current.voice.errorMessage).toMatch(/microphone|permission/i);
  });

  it("commits final speech but never commits interim speech", () => {
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    let recognition: TestRecognition | undefined;
    class TestRecognition {
      onresult: ((event: unknown) => void) | null = null;
      onerror: (() => void) | null = null;
      onend: (() => void) | null = null;
      continuous = false;
      interimResults = false;
      lang = "";
      constructor() {
        recognition = this;
      }
      start() {}
      stop() {
        this.onend?.();
      }
    }
    Object.defineProperty(window, "SpeechRecognition", {
      configurable: true,
      value: TestRecognition,
    });
    const { result } = renderHook(() => {
      const [draft, setDraft] = useState("Typed draft");
      return { draft, voice: useVoiceInput(draft, setDraft) };
    });
    act(() => result.current.voice.toggle());
    act(() =>
      recognition?.onresult?.({
        resultIndex: 0,
        results: [{ 0: { transcript: "maybe" }, isFinal: false }],
      }),
    );
    expect(result.current.draft).toBe("Typed draft");
    expect(result.current.voice.interimTranscript).toBe("maybe");
    act(() =>
      recognition?.onresult?.({
        resultIndex: 0,
        results: [{ 0: { transcript: "final" }, isFinal: true }],
      }),
    );
    expect(result.current.draft).toBe("Typed draft final");
    act(() => result.current.voice.stop());
    expect(result.current.voice.isListening).toBe(false);
  });

  it("explains unsupported speech without changing the draft", () => {
    const { result } = renderHook(() => {
      const [draft, setDraft] = useState("Typed draft");
      return { draft, voice: useVoiceInput(draft, setDraft) };
    });
    act(() => result.current.voice.toggle());
    expect(result.current.draft).toBe("Typed draft");
    expect(result.current.voice.errorMessage).toMatch(/not available|unsupported/i);
  });

  it("uses the prefixed browser API and handles an asynchronous permission denial", () => {
    Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
    let recognition: PrefixedRecognition | undefined;
    class PrefixedRecognition {
      onresult = null;
      onend: (() => void) | null = null;
      onerror: ((event: { error: string }) => void) | null = null;
      continuous = false;
      interimResults = false;
      lang = "";
      constructor() {
        recognition = this;
      }
      start() {}
      stop() {}
    }
    Object.defineProperty(window, "webkitSpeechRecognition", {
      configurable: true,
      value: PrefixedRecognition,
    });
    const { result } = renderHook(() => {
      const [draft, setDraft] = useState("Typed draft");
      return { draft, voice: useVoiceInput(draft, setDraft) };
    });
    act(() => result.current.voice.toggle());
    expect(result.current.voice.isListening).toBe(true);
    act(() => recognition?.onerror?.({ error: "not-allowed" }));
    expect(result.current.voice.isListening).toBe(false);
    expect(result.current.draft).toBe("Typed draft");
    expect(result.current.voice.errorMessage).toMatch(/permission/i);
  });
});
