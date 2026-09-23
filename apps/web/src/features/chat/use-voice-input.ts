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
  onerror: (() => void) | null;
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
  const recognitionRef = useRef<SpeechRecognitionLike | undefined>(undefined);
  const valueRef = useRef(value);
  valueRef.current = value;

  useEffect(() => {
    setIsSupported(Boolean(window.SpeechRecognition ?? window.webkitSpeechRecognition));
    return () => recognitionRef.current?.stop();
  }, []);

  const stop = useCallback(() => {
    recognitionRef.current?.stop();
    recognitionRef.current = undefined;
    setIsListening(false);
  }, []);

  const toggle = useCallback(() => {
    if (recognitionRef.current) {
      stop();
      return;
    }

    const SpeechRecognition = window.SpeechRecognition ?? window.webkitSpeechRecognition;
    if (!SpeechRecognition) return;

    const recognition = new SpeechRecognition();
    const prefix = valueRef.current.trimEnd();
    let finalText = "";
    recognition.continuous = false;
    recognition.interimResults = true;
    recognition.lang = navigator.language || "en-US";
    recognition.onresult = (event) => {
      let interimText = "";
      for (let index = event.resultIndex; index < event.results.length; index += 1) {
        const result = event.results[index];
        if (!result) continue;
        if (result.isFinal) finalText += result[0].transcript;
        else interimText += result[0].transcript;
      }
      onChange(joinTranscript(prefix, finalText + interimText));
    };
    recognition.onend = () => {
      recognitionRef.current = undefined;
      setIsListening(false);
    };
    recognition.onerror = recognition.onend;
    recognitionRef.current = recognition;
    recognition.start();
    setIsListening(true);
  }, [onChange, stop]);

  return { isListening, isSupported, stop, toggle };
}

export function joinTranscript(prefix: string, transcript: string) {
  return prefix ? `${prefix} ${transcript}`.trimEnd() : transcript.trimStart();
}
