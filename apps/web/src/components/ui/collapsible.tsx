// SPDX-License-Identifier: Apache-2.0

import * as CollapsiblePrimitive from "@radix-ui/react-collapsible";
import * as React from "react";

/** The open state Radix keeps private, shared with `CollapsibleContent` so a
 *  find-in-page match can open the collapsible it landed in. */
const CollapsibleStateContext = React.createContext<{
  open: boolean;
  setOpen: (open: boolean) => void;
} | null>(null);

function Collapsible({
  defaultOpen = false,
  onOpenChange,
  open: openProp,
  ...props
}: React.ComponentProps<typeof CollapsiblePrimitive.Root>) {
  const [uncontrolledOpen, setUncontrolledOpen] = React.useState(defaultOpen);
  const open = openProp ?? uncontrolledOpen;
  const setOpen = React.useCallback(
    (next: boolean) => {
      if (openProp === undefined) setUncontrolledOpen(next);
      onOpenChange?.(next);
    },
    [onOpenChange, openProp],
  );
  const state = React.useMemo(() => ({ open, setOpen }), [open, setOpen]);
  return (
    <CollapsibleStateContext.Provider value={state}>
      <CollapsiblePrimitive.Root
        data-slot="collapsible"
        onOpenChange={setOpen}
        open={open}
        {...props}
      />
    </CollapsibleStateContext.Provider>
  );
}

function CollapsibleTrigger({
  ...props
}: React.ComponentProps<typeof CollapsiblePrimitive.CollapsibleTrigger>) {
  return <CollapsiblePrimitive.CollapsibleTrigger data-slot="collapsible-trigger" {...props} />;
}

/**
 * The collapsible's content. With `hiddenUntilFound`, closed content stays in
 * the document as `hidden="until-found"` instead of unmounting, so the
 * browser's find-in-page searches it; on a match the browser fires
 * `beforematch`, which opens the collapsible before it reveals the text.
 * Radix drops closed children even under `forceMount` unless it treats the
 * content as present, and React writes `hidden` only as a boolean, so the
 * attribute and the listener are set on the node directly. Closed content
 * still lays out its own box (`content-visibility: hidden`), so keep its
 * margin and padding on the children.
 */
function CollapsibleContent({
  forceMount,
  hiddenUntilFound = false,
  ref,
  ...props
}: React.ComponentProps<typeof CollapsiblePrimitive.CollapsibleContent> & {
  hiddenUntilFound?: boolean;
}) {
  const state = React.useContext(CollapsibleStateContext);
  const open = state?.open ?? false;
  const setOpen = state?.setOpen;
  const nodeRef = React.useRef<HTMLDivElement | null>(null);
  const setNode = React.useCallback(
    (node: HTMLDivElement | null) => {
      nodeRef.current = node;
      if (typeof ref === "function") ref(node);
      else if (ref) ref.current = node;
    },
    [ref],
  );

  React.useLayoutEffect(() => {
    const node = nodeRef.current;
    if (!hiddenUntilFound || !node) return;
    if (open) node.removeAttribute("hidden");
    else node.setAttribute("hidden", "until-found");
  }, [hiddenUntilFound, open]);

  React.useEffect(() => {
    const node = nodeRef.current;
    if (!hiddenUntilFound || !node || !setOpen) return;
    const reveal = () => setOpen(true);
    node.addEventListener("beforematch", reveal);
    return () => node.removeEventListener("beforematch", reveal);
  }, [hiddenUntilFound, setOpen]);

  return (
    <CollapsiblePrimitive.CollapsibleContent
      data-slot="collapsible-content"
      forceMount={hiddenUntilFound ? true : forceMount}
      ref={setNode}
      {...props}
    />
  );
}

export { Collapsible, CollapsibleContent, CollapsibleTrigger };
