package httpapi

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
)

const (
	specPath          = "../../../openapi/v1.yaml"
	forbiddenResponse = "#/components/responses/Forbidden"
	adminExtension    = "x-admin"
)

func goOperationID(specID string) string {
	if specID == "" {
		return ""
	}
	return strings.ToUpper(specID[:1]) + specID[1:]
}

func adminSpecProblems(doc *openapi3.T, operations map[string]adminRoute) []string {
	var problems []string
	marked := map[string]adminRoute{}
	known := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			id := goOperationID(op.OperationID)
			known[id] = true
			flag, hasFlag := op.Extensions[adminExtension]
			isAdmin := flag == true
			if hasFlag && !isAdmin {
				problems = append(problems, fmt.Sprintf("%s: %s must be true or absent, got %v", id, adminExtension, flag))
			}
			forbids := false
			for _, response := range op.Responses.Map() {
				if response.Ref == forbiddenResponse {
					forbids = true
				}
			}
			switch {
			case forbids && !isAdmin:
				problems = append(problems, fmt.Sprintf("%s: can answer %s but is not marked %s", id, forbiddenResponse, adminExtension))
			case isAdmin && !forbids:
				problems = append(problems, fmt.Sprintf("%s: is marked %s but does not answer %s", id, adminExtension, forbiddenResponse))
			}
			if isAdmin {
				marked[id] = adminRoute{method: method, pattern: "/v1" + path}
			}
		}
	}
	for id, route := range marked {
		got, ok := operations[id]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s: marked %s in the spec but missing from adminOperations", id, adminExtension))
		case got != route:
			problems = append(problems, fmt.Sprintf("%s: adminOperations has %s %s, the spec has %s %s", id, got.method, got.pattern, route.method, route.pattern))
		}
	}
	for id := range operations {
		_, isMarked := marked[id]
		switch {
		case !known[id]:
			problems = append(problems, fmt.Sprintf("%s: in adminOperations but not an operation in the spec", id))
		case !isMarked:
			problems = append(problems, fmt.Sprintf("%s: in adminOperations but not marked %s in the spec", id, adminExtension))
		}
	}
	slices.Sort(problems)
	return problems
}

func unroutedAdminOperations(mux *chi.Mux, operations map[string]adminRoute) []string {
	var missing []string
	for id, route := range operations {
		if mux.Find(chi.NewRouteContext(), route.method, route.pattern) != route.pattern {
			missing = append(missing, id)
		}
	}
	slices.Sort(missing)
	return missing
}

func TestAdminOperationsMatchTheSpec(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	if problems := adminSpecProblems(doc, adminOperations); len(problems) > 0 {
		t.Errorf("admin operations disagree with %s:\n%s", specPath, strings.Join(problems, "\n"))
	}
}

func TestAdminOperationsAreRouted(t *testing.T) {
	h := NewHandler(fakeStore{}, slog.New(slog.DiscardHandler), "test")
	mux, ok := NewRouter(h, slog.New(slog.DiscardHandler)).(*chi.Mux)
	if !ok {
		t.Fatal("NewRouter does not return a *chi.Mux")
	}
	if missing := unroutedAdminOperations(mux, adminOperations); len(missing) > 0 {
		t.Errorf("admin operations with no route: %v", missing)
	}
	if got := unroutedAdminOperations(mux, map[string]adminRoute{
		"GetMe":     {method: "GET", pattern: "/v1/me"},
		"WrongVerb": {method: "POST", pattern: "/v1/me"},
		"NoRoute":   {method: "GET", pattern: "/v1/admin/nothing"},
		"NoPrefix":  {method: "GET", pattern: "/me"},
	}); !slices.Equal(got, []string{"NoPrefix", "NoRoute", "WrongVerb"}) {
		t.Errorf("unrouted = %v, want NoPrefix, NoRoute and WrongVerb", got)
	}
}

const adminSpecFixture = `openapi: 3.0.3
info: {title: t, version: "1"}
paths:
  /admin/things/{id}:
    post:
      operationId: approveThing
      x-admin: true
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      responses:
        "403": {$ref: "#/components/responses/Forbidden"}
  /admin/forgotten:
    get:
      operationId: listForgotten
      responses:
        "403": {$ref: "#/components/responses/Forbidden"}
  /admin/unforbidden:
    get:
      operationId: listUnforbidden
      x-admin: true
      responses:
        "200": {description: ok}
  /admin/stringly:
    get:
      operationId: listStringly
      x-admin: "yes"
      responses:
        "403": {$ref: "#/components/responses/Forbidden"}
  /public:
    get:
      operationId: getPublic
      responses:
        "200": {description: ok}
components:
  responses:
    Forbidden: {description: no}
`

func TestAdminSpecProblemsCatchesMistakes(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData([]byte(adminSpecFixture))
	if err != nil {
		t.Fatal(err)
	}
	got := adminSpecProblems(doc, map[string]adminRoute{
		"ApproveThing":    {method: "POST", pattern: "/v1/admin/things/{id}"},
		"ListUnforbidden": {method: "GET", pattern: "/v1/admin/unforbidden"},
		"GetPublic":       {method: "GET", pattern: "/v1/public"},
		"ApprovThing":     {method: "POST", pattern: "/v1/admin/things/{id}"},
	})
	want := []string{
		"ApprovThing: in adminOperations but not an operation in the spec",
		"GetPublic: in adminOperations but not marked x-admin in the spec",
		"ListForgotten: can answer #/components/responses/Forbidden but is not marked x-admin",
		"ListStringly: can answer #/components/responses/Forbidden but is not marked x-admin",
		"ListStringly: x-admin must be true or absent, got yes",
		"ListUnforbidden: is marked x-admin but does not answer #/components/responses/Forbidden",
	}
	if !slices.Equal(got, want) {
		t.Errorf("problems:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	wrongRoute := adminSpecProblems(doc, map[string]adminRoute{"ApproveThing": {method: "GET", pattern: "/v1/admin/things/{id}"}})
	if !slices.Contains(wrongRoute, "ApproveThing: adminOperations has GET /v1/admin/things/{id}, the spec has POST /v1/admin/things/{id}") {
		t.Errorf("wrong route not reported: %v", wrongRoute)
	}
	missing := adminSpecProblems(doc, map[string]adminRoute{})
	if !slices.Contains(missing, "ApproveThing: marked x-admin in the spec but missing from adminOperations") ||
		!slices.Contains(missing, "ListUnforbidden: marked x-admin in the spec but missing from adminOperations") {
		t.Errorf("missing entries not reported: %v", missing)
	}
}
