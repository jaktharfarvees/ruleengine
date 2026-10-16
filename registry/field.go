package registry

import (
	"time"

	"github.com/arrowhr/ruleengine/rule"
)

// DataType is a closed field value type.
type DataType string

const (
	// DataString is UTF-8 text.
	DataString DataType = "string"
	// DataInteger is an integral number.
	DataInteger DataType = "integer"
	// DataDecimal is an exact decimal number.
	DataDecimal DataType = "decimal"
	// DataBoolean is true or false.
	DataBoolean DataType = "boolean"
	// DataDate is an ISO calendar date.
	DataDate DataType = "date"
	// DataDateTime is an RFC 3339 instant or time.Time value.
	DataDateTime DataType = "datetime"
	// DataEnum is a string from AllowedValues.
	DataEnum DataType = "enum"
	// DataMoney is an exact decimal monetary amount.
	DataMoney DataType = "money"
)

// Role identifies how a registered field participates in evaluation.
type Role string

const (
	// RoleInput is a declared rule input.
	RoleInput Role = "input"
	// RoleOutput is a declared rule output.
	RoleOutput Role = "output"
	// RoleSubjectAttribute is available to scope filters.
	RoleSubjectAttribute Role = "subjectAttribute"
)

// FieldDefinition is the immutable contract for one named field.
type FieldDefinition struct {
	Name              string         `json:"name"`
	Domain            string         `json:"domain"`
	DataType          DataType       `json:"dataType"`
	Unit              string         `json:"unit,omitempty"`
	AllowedValues     []string       `json:"allowedValues,omitempty"`
	PII               bool           `json:"pii"`
	OwnerService      string         `json:"ownerService"`
	Description       string         `json:"description,omitempty"`
	Status            string         `json:"status"`
	Role              Role           `json:"role"`
	FactSource        FactSource     `json:"factSource,omitempty"`
	FactSourceVersion string         `json:"factSourceVersion,omitempty"`
	Effective         rule.Effective `json:"effective"`
	UpdatedBy         string         `json:"updatedBy,omitempty"`
	UpdatedAt         time.Time      `json:"updatedAt,omitempty"`
}
