package sinatra

// api.gen.go is generated from openapi.json by the pinned oapi-codegen, with
// the settings in codegen/. Never edit it by hand: change the spec or the
// settings and run `go generate`. CI fails if the committed file differs.
//
//go:generate bash scripts/derive-templates.sh
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config codegen/config.yaml openapi.json
