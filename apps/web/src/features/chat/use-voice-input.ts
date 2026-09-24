// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useRef, useState } from "react";

interface SpeechRecognitionResultLike {
  0: { transcript: string };
  isFinal: boolean;
}

interface SpeechRecognitionEventLike {
  resultIndex: number;
  results: ArrayLike<SpeechRecognitionResultLike>;
}

interface SpeechRecognitionLike {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  onend: (() => void) | null;
  onerror: ((event: { error?: string }) => void) | null;
  onresult: ((event: SpeechRecognitionEventLike) => void) | null;
  start: () => void;
  stop: () => void;
}

type SpeechRecognitionConstructor = new () => SpeechRecognitionLike;

declare global {
  interface Window {
    SpeechRecognition?: SpeechRecognitionConstructor;
    webkitSpeechRecognition?: SpeechRecognitionConstructor;
  }
}

export function useVoiceInput(value: string, onChange: (value: string) => void) {
  const [isListening, setIsListening] = useState(false);
  const [isSupported, setIsSupported] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [interimTranscript, setInterimTranscript] = useState("");
  const recognitionRef = useRef<SpeechRecognitionLike | undefined>(undefined);
  const valueRef = useRef(value);
  valueRef.current = value;

  useEffect(() => {
    setIsSupported(
      window.isSecureContext !== false &&
        Boolean(window.SpeechRecognition ?? window.webkitSpeechRecognition),
    );
    return () => recognitionRef.current?.stop();
  }, []);

  const stop = useCallback(() => {
    const recognition = recognitionRef.current;
    recognitionRef.current = undefined;
    setIsListening(false);
    setInterimTranscript("");
    recognition?.stop();
  }, []);

  const toggle = useCallback(() => {
    if (recognitionRef.current) {
      stop();
      return;
    }

    setErrorMessage(null);
    if (window.isSecureContext === false) {
      setErrorMessage("Dictation requires a secure connection. Type your message instead.");
      return;
    }
    const SpeechRecognition = window.SpeechRecognition ?? window.webkitSpeechRecognition;
    if (!SpeechRecognition) {
      setErrorMessage(
        "Speech recognition is not available in this browser. Type your message instead.",
      );
      return;
    }

    let recognition: SpeechRecognitionLike;
    try {
      recognition = new SpeechRecognition();
    } catch (error) {
      setErrorMessage(speechFailureMessage(error));
      return;
    }
    const prefix = valueRef.current.trimEnd();
    recognition.continuous = false;
    recognition.interimResults = true;
    recognition.lang = navigator.language || "en-US";
    recognition.onresult = (event) => {
      let finalText = "";
      let interimText = "";
      for (let index = 0; index < event.results.length; index += 1) {
        const result = event.results[index];
        if (!result) continue;
        if (result.isFinal) finalText += result[0].transcript;
        else interimText += result[0].transcript;
      }
      if (finalText) onChange(joinTranscript(prefix, finalText));
      setInterimTranscript(interimText);
    };
    recognition.onend = () => {
      if (recognitionRef.current !== recognition) return;
      recognitionRef.current = undefined;
      setIsListening(false);
      setInterimTranscript("");
    };
    recognition.onerror = (event) => {
      setErrorMessage(speechFailureMessage(event.error));
      recognition.onend?.();
    };
    try {
      recognition.start();
      recognitionRef.current = recognition;
      setIsListening(true);
    } catch (error) {
      setErrorMessage(speechFailureMessage(error));
      setIsListening(false);
    }
  }, [onChange, stop]);

  return { errorMessage, interimTranscript, isListening, isSupported, stop, toggle };
}

function speechFailureMessage(error: unknown): string {
  const reason =
    typeof error === "string"
      ? error
      : typeof error === "object" && error !== null && "name" in error
        ? String(error.name)
        : "";
  if (
    [
      "NotAllowedError",
      "SecurityError",
      "not-allowed",
      "service-not-allowed",
      "audio-capture",
    ].includes(reason)
  ) {
    return "Microphone permission was denied or unavailable. Type your message instead.";
  }
  return "Dictation could not start or continue. Type your message instead.";
}

export function joinTranscript(prefix: string, transcript: string) {
  return prefix ? `${prefix} ${transcript}`.trimEnd() : transcript.trimStart();
}
