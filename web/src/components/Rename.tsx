import { useState } from "react";
import { patch } from "../lib/api";
import { IconCheck, IconEdit, IconX } from "./Icons";

/** Inline rename: shows the name with a pencil; editing saves on Enter. */
export function RenameDevice({ id, name, customName, onSaved }: { id: number; name: string; customName: string; onSaved: () => void }) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);

  const save = () => {
    patch(`/api/v1/devices/${id}`, { name: value.trim() })
      .then(() => { setEditing(false); setError(null); onSaved(); })
      .catch((e: Error) => setError(e.message));
  };

  if (!editing) {
    return (
      <span className="rename">
        <span>{name}</span>
        <button type="button" className="icon-btn subtle" title="Rename" aria-label={`Rename ${name}`}
          onClick={(e) => { e.preventDefault(); e.stopPropagation(); setValue(customName || name); setEditing(true); }}>
          <IconEdit />
        </button>
      </span>
    );
  }
  return (
    <span className="rename editing" onClick={(e) => { e.preventDefault(); e.stopPropagation(); }}>
      <input className="input" autoFocus value={value} maxLength={80} aria-label="Device name"
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => { if (e.key === "Enter") save(); if (e.key === "Escape") setEditing(false); }} />
      <button type="button" className="icon-btn" title="Save" aria-label="Save name" onClick={save}><IconCheck /></button>
      <button type="button" className="icon-btn" title="Cancel" aria-label="Cancel" onClick={() => setEditing(false)}><IconX /></button>
      {error && <span className="error-inline">{error}</span>}
    </span>
  );
}
