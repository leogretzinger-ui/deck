import type { GitIdentityCatalog, GitIdentityProfile } from "../../api/projects.ts";

export type IdentityNeed =
  | { kind: "apply"; profile: GitIdentityProfile }
  | { kind: "create" }
  | { kind: "choose" };

// silentGitIdentity is the identity a project gets without asking: the
// default, or the only one there is.
export function silentGitIdentity(catalog: GitIdentityCatalog | null): GitIdentityProfile | null {
  if (catalog === null) return null;
  return catalog.profiles.find((profile) => profile.id === catalog.default_profile_id)
    ?? (catalog.profiles.length === 1 ? catalog.profiles[0]! : null);
}

// identityNeed says how a project without an identity gets one. Only a
// real choice asks.
export function identityNeed(catalog: GitIdentityCatalog | null): IdentityNeed {
  const profile = silentGitIdentity(catalog);
  if (profile !== null) return { kind: "apply", profile };
  return (catalog?.profiles.length ?? 0) === 0 ? { kind: "create" } : { kind: "choose" };
}

// prefillIdentity fills a draft's empty fields from the GitHub account.
// A field you have touched stays as it is, even when it is empty.
export function prefillIdentity(
  draft: { name: string; email: string },
  github: { name: string; email: string } | null,
  touched: { name: boolean; email: boolean } = { name: false, email: false },
): { name: string; email: string } {
  if (github === null) return draft;
  return {
    name: touched.name || draft.name.trim() !== "" ? draft.name : github.name,
    email: touched.email || draft.email.trim() !== "" ? draft.email : github.email,
  };
}
