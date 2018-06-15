package main

import (
	"context"
	"net/http"

	"github.com/samsarahq/thunder/graphql"
	"github.com/samsarahq/thunder/graphql/graphiql"
	"github.com/samsarahq/thunder/graphql/introspection"
	"github.com/samsarahq/thunder/graphql/schemabuilder"
)

type Server struct {
}

type messageEnumType int32
type User struct {
	FirstName string
	LastName  string
	Ident     messageEnumType
}

func (s *Server) registerUser(schema *schemabuilder.Schema) {
	object := schema.Object("User", User{})

	object.FieldFunc("fullName", func(u *User) string {
		return u.FirstName + " " + u.LastName
	})
}

func (s *Server) registerQuery(schema *schemabuilder.Schema) {
	object := schema.Query()

	var tmp messageEnumType
	schema.EnumReg(tmp, map[string]interface{}{
		"first":  messageEnumType(1),
		"second": messageEnumType(2),
		"third":  messageEnumType(3),
	})

	object.FieldFunc("users", func(ctx context.Context, args struct {
		EnumField messageEnumType
	}) ([]*User, error) {
		return []*User{
			{
				FirstName: "Bob",
				LastName:  "Johnson",
				Ident:     args.EnumField,
			},
			{
				FirstName: "Chloe",
				LastName:  "Kim",
				Ident:     args.EnumField,
			},
		}, nil
	})
}

func (s *Server) registerMutation(schema *schemabuilder.Schema) {
	object := schema.Mutation()

	object.FieldFunc("echo", func(ctx context.Context, args struct {
		Text      string
		EnumField messageEnumType
	}) (messageEnumType, error) {
		return args.EnumField, nil
	})
}

func (s *Server) Schema() *graphql.Schema {
	schema := schemabuilder.NewSchema()

	s.registerUser(schema)
	s.registerQuery(schema)
	s.registerMutation(schema)

	return schema.MustBuild()
}

func main() {
	server := &Server{}
	graphqlSchema := server.Schema()
	introspection.AddIntrospectionToSchema(graphqlSchema)

	http.Handle("/graphql", graphql.Handler(graphqlSchema))
	http.Handle("/graphiql/", http.StripPrefix("/graphiql/", graphiql.Handler()))

	if err := http.ListenAndServe(":3030", nil); err != nil {
		panic(err)
	}
}
