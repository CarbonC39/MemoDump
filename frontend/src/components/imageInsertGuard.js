// An image is hashed and durably staged asynchronously. Bind the eventual node
// insertion to the document that initiated that work, since MemoDump reuses a
// single Milkdown instance across note switches.
export function imageInsertStillCurrent({
  documentVersion,
  currentDocumentVersion,
  activeDocumentVersion,
  destroyed,
  active = true,
}) {
  return active && !destroyed &&
    documentVersion === currentDocumentVersion &&
    documentVersion === activeDocumentVersion
}
