package query

import (
	"fmt"
	"regexp"
	"strconv"
)

type ExpectedOperand struct {
	Msg string
}

func (e *ExpectedOperand) Error() string {
	return e.Msg
}

type UnsupportedError struct {
	Msg string
}

func (e *UnsupportedError) Error() string {
	return e.Msg
}

type SearchType int

const (
	SearchTypeRegex SearchType = iota
	SearchTypeLiteral
	SearchTypeStructural
)

// QueryInfo is an interface for accessing query values that drive our search logic.
// It will be removed in favor of a cleaner query API to access values.
type QueryInfo interface {
	Count() *int
	Archived() *YesNoOnly
	RegexpPatterns(field string) (values, negatedValues []string)
	StringValues(field string) (values, negatedValues []string)
	StringValue(field string) (value, negatedValue string)
	Values(field string) []*Value
	Fields() map[string][]*Value
	BoolValue(field string) bool
	IsCaseSensitive() bool
}

// A query is a tree of Nodes. We choose the type name Q so that external uses like query.Q do not stutter.
type Q []Node

func (q Q) String() string {
	return toString(q)
}

type AST []*Basic

// Basic represents a leaf expression that we can evaluate in our search engine.
// A basic query comprises (1) a single search pattern expression and (2)
// parameters that scope the evaluation of search patterns (e.g., to repos,
// files, etc.).
type Basic struct {
	Pattern    Node
	Parameters []Parameter
}

func (q *Basic) RegexpPatterns(field string) (values, negatedValues []string) {
	VisitField(BasicToParseTree(q), field, func(visitedValue string, negated bool, _ Annotation) {
		if negated {
			negatedValues = append(negatedValues, visitedValue)
		} else {
			values = append(values, visitedValue)
		}
	})
	return values, negatedValues
}

func (q *Basic) StringValues(field string) (values, negatedValues []string) {
	VisitField(BasicToParseTree(q), field, func(visitedValue string, negated bool, _ Annotation) {
		if negated {
			negatedValues = append(negatedValues, visitedValue)
		} else {
			values = append(values, visitedValue)
		}
	})
	return values, negatedValues
}

func (q *Basic) StringValue(field string) (value, negatedValue string) {
	VisitField(BasicToParseTree(q), field, func(visitedValue string, negated bool, _ Annotation) {
		if negated {
			negatedValue = visitedValue
		} else {
			value = visitedValue
		}
	})
	return value, negatedValue
}

func (q *Basic) Values(field string) []*Value {
	var values []*Value
	nodes := BasicToParseTree(q)
	if field == "" {
		VisitPattern(nodes, func(value string, _ bool, annotation Annotation) {
			values = append(values, valueToTypedValue(field, value, annotation.Labels)...)
		})
	} else {
		VisitField(nodes, field, func(value string, _ bool, _ Annotation) {
			values = append(values, valueToTypedValue(field, value, None)...)
		})
	}
	return values
}

func (q *Basic) Fields() map[string][]*Value {
	fields := make(map[string][]*Value)
	nodes := BasicToParseTree(q)
	VisitPattern(nodes, func(value string, _ bool, _ Annotation) {
		fields[""] = q.Values("")
	})
	VisitParameter(nodes, func(field, _ string, _ bool, _ Annotation) {
		fields[field] = q.Values(field)
	})
	return fields
}

func (q *Basic) BoolValue(field string) bool {
	result := false
	VisitField(BasicToParseTree(q), field, func(value string, _ bool, _ Annotation) {
		result, _ = parseBool(value) // err was checked during parsing and validation.
	})
	return result
}

func (q *Basic) Count() *int {
	var count *int
	VisitField(BasicToParseTree(q), FieldCount, func(value string, _ bool, _ Annotation) {
		c, err := strconv.Atoi(value)
		if err != nil {
			panic(fmt.Sprintf("Value %q for count cannot be parsed as an int: %s", value, err))
		}
		count = &c
	})
	return count
}

func (q *Basic) Archived() *YesNoOnly {
	return q.yesNoOnlyValue(FieldArchived)
}

func (q *Basic) yesNoOnlyValue(field string) *YesNoOnly {
	var res *YesNoOnly
	VisitField(BasicToParseTree(q), field, func(value string, _ bool, _ Annotation) {
		yno := ParseYesNoOnly(value)
		if yno == Invalid {
			panic(fmt.Sprintf("Invalid value %q for field %q", value, field))
		}
		res = &yno
	})
	return res
}

func (q *Basic) IsCaseSensitive() bool {
	return q.BoolValue("case")
}

func parseRegexpOrPanic(field, value string) *regexp.Regexp {
	r, err := regexp.Compile(value)
	if err != nil {
		panic(fmt.Sprintf("Value %s for field %s invalid regex: %s", field, value, err.Error()))
	}
	return r
}

// valueToTypedValue approximately preserves the field validation of our
// previous query processing. It does not check the validity of field negation
// or if the same field is specified more than once. This role is now performed
// by validate.go.
func valueToTypedValue(field, value string, label labels) []*Value {
	switch field {
	case
		FieldDefault:
		if label.isSet(Literal) {
			return []*Value{{String: &value}}
		}
		if label.isSet(Regexp) {
			regexp, err := regexp.Compile(value)
			if err != nil {
				panic(fmt.Sprintf("Invariant broken: value must have been checked to be valid regexp. Error: %s", err))
			}
			return []*Value{{Regexp: regexp}}
		}
		// All patterns should have a label after parsing, but if not, treat the pattern as a string literal.
		return []*Value{{String: &value}}

	case
		FieldCase:
		b, _ := parseBool(value)
		return []*Value{{Bool: &b}}

	case
		FieldRepo, "r":
		return []*Value{{Regexp: parseRegexpOrPanic(field, value)}}

	case
		FieldRepoGroup, "g",
		FieldContext:
		return []*Value{{String: &value}}

	case
		FieldFile, "f":
		return []*Value{{Regexp: parseRegexpOrPanic(field, value)}}

	case
		FieldFork,
		FieldArchived,
		FieldLang, "l", "language",
		FieldType,
		FieldPatternType,
		FieldContent:
		return []*Value{{String: &value}}

	case FieldRepoHasFile:
		return []*Value{{Regexp: parseRegexpOrPanic(field, value)}}

	case
		FieldRepoHasCommitAfter,
		FieldBefore, "until",
		FieldAfter, "since":
		return []*Value{{String: &value}}

	case
		FieldAuthor,
		FieldCommitter,
		FieldMessage, "m", "msg":
		return []*Value{{Regexp: parseRegexpOrPanic(field, value)}}

	case
		FieldIndex,
		FieldCount,
		FieldMax,
		FieldTimeout,
		FieldCombyRule:
		return []*Value{{String: &value}}
	}
	return []*Value{{String: &value}}
}
