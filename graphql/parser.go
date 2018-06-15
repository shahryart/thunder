package graphql

import (
	"reflect"
	"strconv"

	"github.com/graphql-go/graphql/language/ast"
	"github.com/graphql-go/graphql/language/parser"
)

// This file contains code to parse GraphQL queries. It reuses the GraphQL
// parser from graphql-go, and stores its output value in a more convenient
// format.

// valueToJson takes a graphql-go ast value and converts it to a value like
// those generated by json.Unmarshal
func valueToJson(value ast.Value, vars map[string]interface{}) (interface{}, error) {
	switch value := value.(type) {
	case *ast.IntValue:
		v, err := strconv.ParseInt(value.Value, 10, 64)
		if err != nil {
			return nil, NewClientError("bad int arg: %s", err)
		}
		return float64(v), nil

	case *ast.FloatValue:
		v, err := strconv.ParseFloat(value.Value, 64)
		if err != nil {
			return nil, NewClientError("bad float arg: %s", err)
		}
		return v, nil
	case *ast.StringValue:
		return value.Value, nil
	case *ast.BooleanValue:
		return value.Value, nil
	case *ast.EnumValue:
		//BOX IT UP HERE (SAME AS THE BOX THAT GETS THE TYPE IN FIELD FUNC)
		return value.Value, nil
	case *ast.Variable:
		actual, ok := vars[value.Name.Value]
		if !ok {
			return nil, nil
		}
		return actual, nil
	case *ast.ObjectValue:
		obj := make(map[string]interface{})
		for _, field := range value.Fields {
			name := field.Name.Value
			if _, found := obj[name]; found {
				return nil, NewClientError("duplicate field")
			}
			value, err := valueToJson(field.Value, vars)
			if err != nil {
				return nil, err
			}
			obj[name] = value
		}
		return obj, nil
	case *ast.ListValue:
		list := make([]interface{}, 0, len(value.Values))
		for _, item := range value.Values {
			value, err := valueToJson(item, vars)
			if err != nil {
				return nil, err
			}
			list = append(list, value)
		}
		return list, nil
	default:
		return nil, NewClientError("unsupported value type: %s", value.GetKind())
	}
}

// argsToJson converts a graphql-go ast argument list to a json.Marshal-style
// map[string]interface{}
func argsToJson(input []*ast.Argument, vars map[string]interface{}) (interface{}, error) {
	args := make(map[string]interface{})
	for _, arg := range input {
		name := arg.Name.Value
		if _, found := args[name]; found {
			return nil, NewClientError("duplicate arg")
		}
		value, err := valueToJson(arg.Value, vars)
		if err != nil {
			return nil, err
		}
		args[name] = value
	}
	return args, nil
}

// parseSelectionSet takes a grapqhl-go selection set and converts it to a
// simplified *SelectionSet, bindings vars
func parseSelectionSet(input *ast.SelectionSet, globalFragments map[string]*Fragment, vars map[string]interface{}) (*SelectionSet, error) {
	if input == nil {
		return nil, nil
	}

	var selections []*Selection
	var fragments []*Fragment
	for _, selection := range input.Selections {
		switch selection := selection.(type) {
		case *ast.Field:
			alias := selection.Name.Value
			if selection.Alias != nil {
				alias = selection.Alias.Value
			}

			if len(selection.Directives) != 0 {
				return nil, NewClientError("directives not supported")
			}

			args, err := argsToJson(selection.Arguments, vars)
			if err != nil {
				return nil, err
			}

			selectionSet, err := parseSelectionSet(selection.SelectionSet, globalFragments, vars)
			if err != nil {
				return nil, err
			}

			selections = append(selections, &Selection{
				Alias:        alias,
				Name:         selection.Name.Value,
				Args:         args,
				SelectionSet: selectionSet,
			})

		case *ast.FragmentSpread:
			name := selection.Name.Value

			if len(selection.Directives) != 0 {
				return nil, NewClientError("directives not supported")
			}

			fragment, found := globalFragments[name]
			if !found {
				return nil, NewClientError("unknown fragment")
			}

			fragments = append(fragments, fragment)

		case *ast.InlineFragment:
			on := selection.TypeCondition.Name.Value

			if len(selection.Directives) != 0 {
				return nil, NewClientError("directives not supported")
			}

			selectionSet, err := parseSelectionSet(selection.SelectionSet, globalFragments, vars)
			if err != nil {
				return nil, err
			}

			fragments = append(fragments, &Fragment{
				On:           on,
				SelectionSet: selectionSet,
			})
		}
	}

	selectionSet := &SelectionSet{
		Selections: selections,
		Fragments:  fragments,
	}
	return selectionSet, nil
}

type visitState int

const (
	none visitState = iota
	visiting
	visited
)

// detectCyclesAndUnusedFragments finds cycles in fragments that include
// eachother as well as fragments that don't appear anywhere
func detectCyclesAndUnusedFragments(selectionSet *SelectionSet, globalFragments map[string]*Fragment) error {
	state := make(map[*Fragment]visitState)

	var visitFragment func(*Fragment) error
	var visitSelectionSet func(*SelectionSet) error

	visitSelectionSet = func(selectionSet *SelectionSet) error {
		if selectionSet == nil {
			return nil
		}

		for _, selection := range selectionSet.Selections {
			if err := visitSelectionSet(selection.SelectionSet); err != nil {
				return err
			}
		}

		for _, fragment := range selectionSet.Fragments {
			if err := visitFragment(fragment); err != nil {
				return err
			}
		}

		return nil
	}

	visitFragment = func(fragment *Fragment) error {
		switch state[fragment] {
		case visiting:
			return NewClientError("fragment contains itself")
		case visited:
			return nil
		}

		state[fragment] = visiting
		if err := visitSelectionSet(fragment.SelectionSet); err != nil {
			return err
		}
		state[fragment] = visited

		return nil
	}

	if err := visitSelectionSet(selectionSet); err != nil {
		return err
	}

	for _, fragment := range globalFragments {
		if state[fragment] != visited {
			return NewClientError("unused fragment")
		}
	}
	return nil
}

// detectConflicts finds conflicts
//
// Conflicts are selections that can not be merged, for example
//
//     foo: bar(id: 123)
//     foo: baz(id: 456)
//
// A query cannot contain both selections, because they have the same alias
// with different source names, and they also have different arguments.
func detectConflicts(selectionSet *SelectionSet) error {
	state := make(map[*SelectionSet]visitState)

	var visitChild func(*SelectionSet) error
	visitChild = func(selectionSet *SelectionSet) error {
		if state[selectionSet] == visited {
			return nil
		}
		state[selectionSet] = visited

		selections := make(map[string]*Selection)

		var visitSibling func(*SelectionSet) error
		visitSibling = func(selectionSet *SelectionSet) error {
			for _, selection := range selectionSet.Selections {
				if other, found := selections[selection.Alias]; found {
					if other.Name != selection.Name {
						return NewClientError("same alias with different name")
					}
					if !reflect.DeepEqual(other.Args, selection.Args) {
						return NewClientError("same alias with different args")
					}
				} else {
					selections[selection.Alias] = selection
				}
			}

			for _, fragment := range selectionSet.Fragments {
				if err := visitSibling(fragment.SelectionSet); err != nil {
					return err
				}
			}

			return nil
		}

		if err := visitSibling(selectionSet); err != nil {
			return err
		}

		return nil
	}

	return visitChild(selectionSet)
}

// determineComplex determines which selection sets are complex
//
// Complex selection sets contain nested selection sets. In
//
//     { organization { group { devices { name } } } }
//
// organization and group are complex, but devices is not as it only contains
// name
func determineComplex(selectionSet *SelectionSet) {
	state := make(map[*SelectionSet]visitState)

	var visit func(*SelectionSet)
	visit = func(selectionSet *SelectionSet) {
		if state[selectionSet] == visited {
			return
		}
		state[selectionSet] = visited

		for _, selection := range selectionSet.Selections {
			if selection.SelectionSet != nil {
				selectionSet.Complex = true
				visit(selection.SelectionSet)
			}
		}

		for _, fragment := range selectionSet.Fragments {
			visit(fragment.SelectionSet)
			if fragment.SelectionSet.Complex {
				selectionSet.Complex = true
			}
		}
	}

	visit(selectionSet)
}

type Query struct {
	Name string
	Kind string
	*SelectionSet
}

// Parse parses an input GraphQL string into a *Query
//
// Parse validates that the query looks syntactically correct and
// contains no cycles or unused fragments or immediate conflicts. However, it
// does not validate that the query is legal under a given schema, which
// instead is done by PrepareQuery.
func Parse(source string, vars map[string]interface{}) (*Query, error) {
	document, err := parser.Parse(parser.ParseParams{Source: source})
	if err != nil {
		return nil, NewClientError(err.Error())
	}

	var queryDefinition *ast.OperationDefinition
	fragmentDefinitions := make(map[string]*ast.FragmentDefinition)

	for _, definition := range document.Definitions {
		switch definition := definition.(type) {
		case *ast.FragmentDefinition:
			name := definition.Name.Value
			if _, found := fragmentDefinitions[name]; found {
				return nil, NewClientError("duplicate fragment")
			}
			fragmentDefinitions[name] = definition

		case *ast.OperationDefinition:
			if definition.Operation != "query" && definition.Operation != "mutation" {
				return nil, NewClientError("only support queries or mutations")
			}
			if queryDefinition != nil {
				return nil, NewClientError("only support a single query")
			}
			queryDefinition = definition

		default:
			return nil, NewClientError("unsupported definition")
		}
	}

	if queryDefinition == nil {
		return nil, NewClientError("must have a single query")
	}

	kind := queryDefinition.Operation
	var name string
	if queryDefinition.Name != nil {
		name = queryDefinition.Name.Value
	}

	rv := &Query{
		Name:         name,
		Kind:         kind,
		SelectionSet: nil,
	}
	// Parse variable definitions, default values, etc.
	var defaultedVars map[string]interface{}
	for _, variableDefinition := range queryDefinition.VariableDefinitions {
		name := variableDefinition.Variable.Name.Value

		if _, ok := variableDefinition.Type.(*ast.NonNull); ok {
			if variableDefinition.DefaultValue != nil {
				return rv, NewClientError("required variable cannot provide a default value: $%s", name)
			}

			continue
		}

		if variableDefinition.DefaultValue != nil {
			// Ignore default if the value exists.
			if vars[name] != nil {
				continue
			}

			// Lazily initialize defaultedVars if needed.
			if defaultedVars == nil {
				defaultedVars = make(map[string]interface{})
				for k, v := range vars {
					defaultedVars[k] = v
				}
			}

			// TODO: properly implement coerceValue.
			// See: https://github.com/graphql/graphql-js/blob/17a0bfd5292f39cafe4eec5b3bd0e22514243b68/src/execution/values.js#L84
			val, err := valueToJson(variableDefinition.DefaultValue, nil)
			if err != nil {
				return rv, NewClientError("failed to parse default value: %s", err.Error())
			}
			defaultedVars[name] = val
		}
	}

	if defaultedVars != nil {
		vars = defaultedVars
	}

	globalFragments := make(map[string]*Fragment)
	for name, fragment := range fragmentDefinitions {
		globalFragments[name] = &Fragment{
			On: fragment.TypeCondition.Name.Value,
		}
	}

	for name, fragment := range fragmentDefinitions {
		selectionSet, err := parseSelectionSet(fragment.SelectionSet, globalFragments, vars)
		if err != nil {
			return rv, err
		}
		globalFragments[name].SelectionSet = selectionSet
	}

	selectionSet, err := parseSelectionSet(queryDefinition.SelectionSet, globalFragments, vars)
	if err != nil {
		return rv, err
	}

	if err := detectCyclesAndUnusedFragments(selectionSet, globalFragments); err != nil {
		return rv, err
	}

	if err := detectConflicts(selectionSet); err != nil {
		return rv, err
	}

	determineComplex(selectionSet)

	rv.SelectionSet = selectionSet

	return rv, nil
}

func MustParse(source string, vars map[string]interface{}) *Query {
	query, err := Parse(source, vars)
	if err != nil {
		panic(err)
	}
	return query
}

// Flatten takes a SelectionSet and flattens it into an array of selections
// with unique aliases
//
// A GraphQL query (the SelectionSet) is allowed to contain the same key
// multiple times, as well as fragments. For example,
//
//     {
//       groups { name }
//       groups { name id }
//       ... on Organization { groups { widgets { name } } }
//     }
//
// Flatten simplifies the query into an array of selections, merging fields,
// resulting in something like the new query
//
//     groups: { name name id { widgets { name } } }
//
// Flatten does _not_ flatten out the inner queries, so the name above does not
// get flattened out yet.
func Flatten(selectionSet *SelectionSet) []*Selection {
	grouped := make(map[string][]*Selection)

	state := make(map[*SelectionSet]visitState)
	var visit func(*SelectionSet)
	visit = func(selectionSet *SelectionSet) {
		if state[selectionSet] == visited {
			return
		}

		for _, selection := range selectionSet.Selections {
			grouped[selection.Alias] = append(grouped[selection.Alias], selection)
		}
		for _, fragment := range selectionSet.Fragments {
			visit(fragment.SelectionSet)
		}

		state[selectionSet] = visited
	}

	visit(selectionSet)

	var flattened []*Selection
	for _, selections := range grouped {
		if len(selections) == 1 || selections[0].SelectionSet == nil {
			flattened = append(flattened, selections[0])
			continue
		}

		merged := &SelectionSet{}
		for _, selection := range selections {
			merged.Selections = append(merged.Selections, selection.SelectionSet.Selections...)
			merged.Fragments = append(merged.Fragments, selection.SelectionSet.Fragments...)
		}

		flattened = append(flattened, &Selection{
			Name:         selections[0].Name,
			Alias:        selections[0].Alias,
			Args:         selections[0].Args,
			SelectionSet: merged,
		})
	}

	return flattened
}

/*
// TODO: precompute flatten
// TODO: properly typecheck fragments
// TODO: correctly check conflicts pre-flatten
*/
