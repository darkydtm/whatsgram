package config

import (
	"reflect"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

func validYAMLIntegers(root *yaml.Node) bool {
	type visit struct {
		node   *yaml.Node
		typeOf reflect.Type
	}
	seen := make(map[visit]uint8)
	var check func(*yaml.Node, reflect.Type) bool
	check = func(node *yaml.Node, typeOf reflect.Type) bool {
		key := visit{node, typeOf}
		if seen[key] != 0 {
			return seen[key] == 2
		}
		seen[key] = 1
		if node.Kind == yaml.AliasNode {
			if node.Alias == nil || !check(node.Alias, typeOf) {
				return false
			}
		} else if typeOf != reflect.TypeOf(time.Duration(0)) {
			switch typeOf.Kind() {
			case reflect.Int, reflect.Int64:
				if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
					return false
				}
				if _, err := strconv.ParseInt(strings.ReplaceAll(node.Value, "_", ""), 0, typeOf.Bits()); err != nil {
					return false
				}
			case reflect.Slice:
				if node.Kind == yaml.SequenceNode {
					for _, child := range node.Content {
						if !check(child, typeOf.Elem()) {
							return false
						}
					}
				}
			case reflect.Struct:
				if node.Kind == yaml.SequenceNode {
					for _, child := range node.Content {
						if !check(child, typeOf) {
							return false
						}
					}
				} else if node.Kind == yaml.MappingNode {
					for i := 0; i+1 < len(node.Content); i += 2 {
						name, value := node.Content[i], node.Content[i+1]
						if name.Tag == "!!merge" {
							if !check(value, typeOf) {
								return false
							}
							continue
						}
						for j := 0; j < typeOf.NumField(); j++ {
							field := typeOf.Field(j)
							if field.Tag.Get("yaml") == name.Value && !check(value, field.Type) {
								return false
							}
						}
					}
				}
			}
		}
		seen[key] = 2
		return true
	}
	return check(root, reflect.TypeOf(Config{}))
}
