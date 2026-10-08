// Package inventory reads what a Terraform root creates and looks up: its
// resources and data sources, the variables that switch things on and off,
// outputs, tags, and where its state lives. It works on the source files alone,
// evaluating variables, locals, and a set of common functions where it can and
// saying so where it can't.
package inventory

// Inventory is everything found in one Terraform root and the local modules it
// calls.
type Inventory struct {
	// Backend and StateKey say where the root's state is kept, so another root
	// reading it through terraform_remote_state can be matched to it.
	Backend   string
	StateKey  string
	Resources []Resource
	Variables []Variable
	Outputs   []Output
}

const (
	ModeManaged = "managed"
	ModeData    = "data"
)

// Enabled values. A resource with a count or for_each that evaluates to zero is
// declared but not created.
const (
	EnabledTrue    = "true"
	EnabledFalse   = "false"
	EnabledUnknown = "unknown"
)

type Resource struct {
	// Address is the full address, "module.net.azurerm_virtual_network.main" or
	// "data.azurerm_resource_group.rg", without any instance index.
	Address string
	Mode    string
	Type    string
	Name    string
	// ModulePath is the dotted path of module calls the resource is in, empty for
	// the root.
	ModulePath string
	Enabled    string
	// Instances is how many copies count or for_each makes, when known.
	Instances *int
	// Condition is the count or for_each expression, when there is one.
	Condition string
	File      string
	Line      int
	// Attrs holds the resolved values of the attributes worth recording: names,
	// locations, sizes, and the like. Secrets are never read.
	Attrs map[string]string
	// Unresolved holds the expression text of recorded attributes whose value
	// depends on something that is only known when Terraform runs.
	Unresolved map[string]string
	Tags       map[string]string
	// TagsUnresolved holds the expression of each tag whose value couldn't be
	// resolved, keyed by tag, or "*" for a tags expression that couldn't be read at all.
	TagsUnresolved map[string]string
}

type Variable struct {
	ModulePath string
	Name       string
	Type       string
	// Value is the value the variable takes, as JSON, empty when unknown or hidden.
	Value string
	// Source says where Value comes from: "default", "tfvars", "input" (passed by
	// the calling module), or "unset".
	Source    string
	Sensitive bool
	// Flag is true for a variable that works as a feature switch: a boolean, or
	// one that decides whether something is created.
	Flag bool
	// Controls lists the addresses of resources and modules whose count or
	// for_each depends on the variable.
	Controls []string
}

type Output struct {
	ModulePath string
	Name       string
	Expr       string
	Sensitive  bool
}
