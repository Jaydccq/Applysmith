import { useEffect, useMemo } from "react";
import { createBoard } from "./api/board";
import { BoardContext } from "./hooks";
import { Shell } from "./Shell";
import { useSignedOut } from "./api/session";
import { SignedOut } from "./ui/SignedOut";

export function App() {
  const board = useMemo(() => createBoard(), []);
  const signedOut = useSignedOut();
  // A refused stream stops; it starts again when a new key brings the page back.
  useEffect(() => {
    if (signedOut) return;
    board.start();
    return () => board.stop();
  }, [board, signedOut]);
  if (signedOut) return <SignedOut />;
  return (
    <BoardContext.Provider value={board}>
      <Shell />
    </BoardContext.Provider>
  );
}
