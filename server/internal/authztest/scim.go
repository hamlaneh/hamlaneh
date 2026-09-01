package authztest

import "sort"

// The SCIM half of the authorization harness. CLAUDE.md requires every
// endpoint to register one entry, and the OpenAPI completeness gate cannot
// see these: SCIM has its own wire format and lives outside the contract on
// purpose (docs/api/scim.md §1). SCIMRegistry is their register, and the
// tests beside it are the gate that keeps three things in step — the
// document's operation table, this registry, and the routes the mux actually
// serves.
//
// It is deliberately the same shape as the WebSocket half in ws.go rather
// than a second style of the same idea: one parser, one diff, one failure
// message a reader has already seen once.

// SCIMOperation identifies one row of the operation table in scim.md §6. The
// op name is the key — it is unique in the document, and it is what a
// failure message reads best as.
type SCIMOperation string

func (op SCIMOperation) String() string { return string(op) }

// SCIMAuthz is the authorization rule an operation must enforce, mirroring
// the authz column of scim.md §6.
type SCIMAuthz string

const (
	// SCIMAuthzUnspecified marks a forgotten decision; completeness fails.
	SCIMAuthzUnspecified SCIMAuthz = ""
	// SCIMBearer needs a live provisioning token, and nothing else.
	// Anonymous, an ordinary session cookie, an administrator's session
	// cookie, and a revoked token all get 401.
	SCIMBearer SCIMAuthz = "bearer"
)

// SCIMAuthzRules returns every rule the document may name.
//
// There is one. The column exists so that adding an operation that is NOT
// bearer-only has to be a visible decision rather than an omission, which is
// the same reason the WS table carries a column with five values it mostly
// does not use.
func SCIMAuthzRules() []SCIMAuthz {
	return []SCIMAuthz{SCIMBearer}
}

// SCIMRow is one parsed row of the operation table in scim.md §6.
type SCIMRow struct {
	Op     SCIMOperation
	Method string
	Path   string
	Authz  SCIMAuthz
}

// SCIMEntry is one SCIM operation's registration.
//
// Unlike WSEntry it carries no status field. Every rule here is asserted for
// real by TestSCIMBearerMatrix, which runs the whole grid of credentials
// against every operation on a real server and a real database; a status
// saying "not asserted yet" would have no member.
type SCIMEntry struct {
	Op     SCIMOperation
	Method string
	Path   string
	Authz  SCIMAuthz
}

// SCIMRegistry returns the SCIM authorization registry: one entry per row of
// the operation table in docs/api/scim.md §6.
//
// Every entry is SCIMBearer, and that uniformity IS the security property
// rather than a shortcut. The provisioning surface has exactly one
// credential; an operation that could be reached with a session cookie would
// mean an administrator's browser could be made to provision accounts, and
// one that could be reached without any credential would mean the directory
// is public. Both are what these rows exist to forbid, on every operation
// including the three discovery documents.
func SCIMRegistry() []SCIMEntry {
	return []SCIMEntry{
		scimEntry("serviceProviderConfig", "GET", "/scim/v2/ServiceProviderConfig"),
		scimEntry("resourceTypes", "GET", "/scim/v2/ResourceTypes"),
		scimEntry("schemas", "GET", "/scim/v2/Schemas"),
		scimEntry("listUsers", "GET", "/scim/v2/Users"),
		scimEntry("createUser", "POST", "/scim/v2/Users"),
		scimEntry("getUser", "GET", "/scim/v2/Users/{id}"),
		scimEntry("replaceUser", "PUT", "/scim/v2/Users/{id}"),
		scimEntry("patchUser", "PATCH", "/scim/v2/Users/{id}"),
		scimEntry("deleteUser", "DELETE", "/scim/v2/Users/{id}"),
	}
}

// scimEntry registers one operation. The rule is not a parameter: there is
// one rule, and a call site that could pass a different one would be a place
// to get it wrong.
func scimEntry(op SCIMOperation, method, path string) SCIMEntry {
	return SCIMEntry{Op: op, Method: method, Path: path, Authz: SCIMBearer}
}

// DiffSCIMRegistry compares the contract document's operation table against
// the registry: missing lists documented operations with no entry (the
// CI-failing case), extra lists entries for operations the document no
// longer has. Both come back sorted for stable failure output.
func DiffSCIMRegistry(rows []SCIMRow, entries []SCIMEntry) (missing, extra []string) {
	inRegistry := map[SCIMOperation]bool{}
	for _, e := range entries {
		inRegistry[e.Op] = true
	}
	inDoc := map[SCIMOperation]bool{}
	for _, row := range rows {
		inDoc[row.Op] = true
		if !inRegistry[row.Op] {
			missing = append(missing, row.Op.String())
		}
	}
	for op := range inRegistry {
		if !inDoc[op] {
			extra = append(extra, op.String())
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}
