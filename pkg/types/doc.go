// Package types holds identifiers and enumerations shared across the Agent
// Console backend — session lifecycle states, provider identifiers, and the
// runtime identity tuple.
//
// This package is for vocabulary that must not drift between the database
// layer, the reconciler, and the API. It deliberately holds no behavior: it
// must not import other internal packages, and it must never become a
// normalized cross-provider event schema, which is explicitly out of scope.
package types
