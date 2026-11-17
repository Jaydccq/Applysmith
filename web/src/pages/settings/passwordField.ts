import { useRef, useState } from "react";

// PasswordField is write-only: uncontrolled, emptied as soon as the request is
// sent, and never given a value to show except the one the generator just made.
export function usePasswordField() {
  const ref = useRef<HTMLInputElement>(null);
  const [text, setText] = useState("");
  return {
    ref,
    text,
    onInput: (value: string) => setText(value),
    take: () => {
      const v = ref.current?.value ?? "";
      if (ref.current) ref.current.value = "";
      setText("");
      return v;
    },
    set: (v: string) => {
      if (ref.current) ref.current.value = v;
      setText(v);
    },
  };
}
