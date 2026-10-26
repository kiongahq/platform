package agentgraph

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Condition is a parsed condition expression (operator:argument).
type Condition struct {
	Op       string
	Argument string
}

// ParseCondition checks an expression against the registry's operators.
// agent-runtime evaluates the same grammar; the gateway only parses it.
func (r *Registry) ParseCondition(expression string) (Condition, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return Condition{}, fmt.Errorf("enter a condition such as contains:refund")
	}
	if len(expression) > 300 {
		return Condition{}, fmt.Errorf("a condition can have at most 300 characters")
	}
	op, argument, hasArgument := strings.Cut(expression, ":")
	op = strings.TrimSpace(op)
	for _, operator := range r.ConditionOperators {
		if operator.Op != op {
			continue
		}
		if operator.Argument && (!hasArgument || argument == "") {
			return Condition{}, fmt.Errorf("%s needs an argument, for example %s:value", op, op)
		}
		if !operator.Argument && hasArgument && strings.TrimSpace(argument) != "" {
			return Condition{}, fmt.Errorf("%s takes no argument", op)
		}
		switch op {
		case "matches":
			if len(argument) > 200 {
				return Condition{}, fmt.Errorf("a pattern can have at most 200 characters")
			}
			if _, err := regexp.Compile(argument); err != nil {
				return Condition{}, fmt.Errorf("invalid pattern: %v", err)
			}
		case "longer_than":
			if value, err := strconv.Atoi(strings.TrimSpace(argument)); err != nil || value < 0 {
				return Condition{}, fmt.Errorf("longer_than needs a whole number")
			}
		}
		return Condition{Op: op, Argument: argument}, nil
	}
	names := make([]string, 0, len(r.ConditionOperators))
	for _, operator := range r.ConditionOperators {
		names = append(names, operator.Op)
	}
	return Condition{}, fmt.Errorf("use operator:argument with one of %s", strings.Join(names, ", "))
}
