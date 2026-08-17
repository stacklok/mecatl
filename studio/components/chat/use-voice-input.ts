"use client";

import { useCallback, useEffect, useRef, useState } from "react";

/**
 * Browser dictation for the composer, via the Web Speech API.
 *
 * Ported from the console's composer. Recognition is NOT continuous: it settles
 * after a natural pause, which suits dictating a prompt rather than transcribing
 * a meeting, and means the microphone is never left open indefinitely.
 *
 * `isSupported` is resolved in an effect rather than during render because the
 * constructor lives on `window` — reading it while rendering would break SSR and
 * cause a hydration mismatch. Support is Chromium/Safari today; Firefox reports
 * unsupported, and the caller hides the affordance rather than offering a button
 * that cannot work.
 */
export function useVoiceInput(onTranscript: (text: string) => void) {
  const [isListening, setIsListening] = useState(false);
  const [isSupported, setIsSupported] = useState(false);
  const recognitionRef = useRef<SpeechRecognitionInstance | null>(null);

  useEffect(() => {
    const SR = typeof window !== "undefined"
      ? window.SpeechRecognition || window.webkitSpeechRecognition
      : null;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setIsSupported(Boolean(SR));
  }, []);

  // Stop the microphone if the composer unmounts mid-dictation; the recogniser
  // holds a live audio capture that would otherwise outlive the component.
  useEffect(() => () => recognitionRef.current?.stop(), []);

  const toggle = useCallback(() => {
    if (isListening) {
      recognitionRef.current?.stop();
      setIsListening(false);
      return;
    }

    const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
    if (!SR) return;

    const recognition = new SR();
    recognition.continuous = false;
    recognition.interimResults = true;
    recognition.lang = "en-US";

    // Interim results stream in and are replaced as the recogniser refines them,
    // so the final text is accumulated separately and the caller is handed
    // "settled so far + current guess" on every event.
    let finalText = "";

    recognition.onresult = (event) => {
      let interim = "";
      for (let index = event.resultIndex; index < event.results.length; index += 1) {
        const result = event.results[index];
        if (result.isFinal) finalText += result[0].transcript;
        else interim += result[0].transcript;
      }
      onTranscript(finalText + interim);
    };

    recognition.onend = () => {
      setIsListening(false);
      if (finalText) onTranscript(finalText);
    };

    recognition.onerror = () => setIsListening(false);

    recognitionRef.current = recognition;
    recognition.start();
    setIsListening(true);
  }, [isListening, onTranscript]);

  return { isListening, isSupported, toggle };
}

type SpeechRecognitionResultLike = {
  isFinal: boolean;
  readonly [index: number]: { transcript: string };
};

type SpeechRecognitionEventLike = {
  resultIndex: number;
  results: { length: number; readonly [index: number]: SpeechRecognitionResultLike };
};

type SpeechRecognitionInstance = {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  onresult: ((event: SpeechRecognitionEventLike) => void) | null;
  onend: (() => void) | null;
  onerror: ((event: Event) => void) | null;
  start: () => void;
  stop: () => void;
};

type SpeechRecognitionConstructor = new () => SpeechRecognitionInstance;

declare global {
  interface Window {
    SpeechRecognition: SpeechRecognitionConstructor;
    webkitSpeechRecognition: SpeechRecognitionConstructor;
  }
}
