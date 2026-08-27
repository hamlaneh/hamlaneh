package scim

import "net/http"

// The three discovery documents (scim.md §2). They are static: what this
// surface supports is decided here and in the code, never per request or per
// instance.
//
// They are raw JSON rather than marshalled structs because nothing in them
// varies — a struct with no dynamic field is a second place for the same
// literal to live, and a provider reads these byte for byte.
//
// The most important thing about them is what is ABSENT. ResourceTypes lists
// User and not Group, and that absence IS how Groups are refused: there is
// no group model in the product (ADR 001), and mapping groups onto nothing
// and answering 200 would be a lie a provider would then build on.

// serviceProviderConfig declares exactly what §2 says it declares. Every
// false is a deliberate refusal rather than a half-built feature:
//
//   - bulk, sort and etag are not needed at instance scale.
//   - changePassword is false because a directory pushing passwords into
//     this system is a credential path nobody asked for; a password
//     attribute in a request body is ignored.
//   - filter is supported, but only for the two equalities §2 names.
const serviceProviderConfig = `{
  "schemas": ["urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"],
  "documentationUri": "https://github.com/hamlaneh/hamlaneh/blob/main/docs/api/scim.md",
  "patch": { "supported": true },
  "bulk": { "supported": false, "maxOperations": 0, "maxPayloadSize": 0 },
  "filter": { "supported": true, "maxResults": 200 },
  "changePassword": { "supported": false },
  "sort": { "supported": false },
  "etag": { "supported": false },
  "authenticationSchemes": [
    {
      "type": "oauthbearertoken",
      "name": "OAuth Bearer Token",
      "description": "A provisioning token minted from the admin dashboard, presented as Authorization: Bearer.",
      "primary": true
    }
  ],
  "meta": { "resourceType": "ServiceProviderConfig", "location": "/scim/v2/ServiceProviderConfig" }
}`

// resourceTypes lists User, and nothing else. See the file comment.
const resourceTypes = `{
  "schemas": ["urn:ietf:params:scim:api:messages:2.0:ListResponse"],
  "totalResults": 1,
  "startIndex": 1,
  "itemsPerPage": 1,
  "Resources": [
    {
      "schemas": ["urn:ietf:params:scim:schemas:core:2.0:ResourceType"],
      "id": "User",
      "name": "User",
      "endpoint": "/Users",
      "description": "User Account",
      "schema": "urn:ietf:params:scim:schemas:core:2.0:User",
      "meta": { "resourceType": "ResourceType", "location": "/scim/v2/ResourceTypes/User" }
    }
  ]
}`

// schemas is the User schema, trimmed to the attributes §4 actually maps.
// Advertising the whole of RFC 7643's User would promise round-tripping for
// attributes this server drops on write and never emits, which is the same
// lie as a fake Group endpoint in a smaller shape.
const schemas = `{
  "schemas": ["urn:ietf:params:scim:api:messages:2.0:ListResponse"],
  "totalResults": 1,
  "startIndex": 1,
  "itemsPerPage": 1,
  "Resources": [
    {
      "id": "urn:ietf:params:scim:schemas:core:2.0:User",
      "name": "User",
      "description": "User Account",
      "attributes": [
        {
          "name": "userName", "type": "string", "multiValued": false,
          "required": true, "caseExact": false, "mutability": "readWrite",
          "returned": "default", "uniqueness": "server"
        },
        {
          "name": "externalId", "type": "string", "multiValued": false,
          "required": false, "caseExact": true, "mutability": "readWrite",
          "returned": "default", "uniqueness": "server"
        },
        {
          "name": "displayName", "type": "string", "multiValued": false,
          "required": false, "caseExact": false, "mutability": "readWrite",
          "returned": "default", "uniqueness": "none"
        },
        {
          "name": "name", "type": "complex", "multiValued": false,
          "required": false, "mutability": "readWrite", "returned": "default",
          "subAttributes": [
            {
              "name": "formatted", "type": "string", "multiValued": false,
              "required": false, "caseExact": false, "mutability": "readWrite",
              "returned": "default", "uniqueness": "none"
            }
          ]
        },
        {
          "name": "emails", "type": "complex", "multiValued": true,
          "required": false, "mutability": "readWrite", "returned": "default",
          "subAttributes": [
            {
              "name": "value", "type": "string", "multiValued": false,
              "required": false, "caseExact": false, "mutability": "readWrite",
              "returned": "default", "uniqueness": "none"
            },
            {
              "name": "primary", "type": "boolean", "multiValued": false,
              "required": false, "mutability": "readWrite", "returned": "default"
            }
          ]
        },
        {
          "name": "active", "type": "boolean", "multiValued": false,
          "required": false, "mutability": "readWrite", "returned": "default"
        }
      ],
      "meta": {
        "resourceType": "Schema",
        "location": "/scim/v2/Schemas/urn:ietf:params:scim:schemas:core:2.0:User"
      }
    }
  ]
}`

func serveServiceProviderConfig(w http.ResponseWriter, r *http.Request) {
	writeStatic(w, r, serviceProviderConfig)
}

func serveResourceTypes(w http.ResponseWriter, r *http.Request) {
	writeStatic(w, r, resourceTypes)
}

func serveSchemas(w http.ResponseWriter, r *http.Request) {
	writeStatic(w, r, schemas)
}

// writeStatic sends one of the fixed documents above. They are already JSON,
// so there is no marshalling step and therefore no error path.
func writeStatic(w http.ResponseWriter, r *http.Request, body string) {
	w.Header().Set("Content-Type", ContentType)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(body)); err != nil {
		logWriteFailure(r, err)
	}
}
