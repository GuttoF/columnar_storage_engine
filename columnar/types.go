package columnar

type DataType uint32

const (
	TypeInt DataType = iota
	TypeDouble
	TypeBool
	TypeString
)

const (
	StringWidth = 64
	MaxLineLen  = 1023
	MaxCols     = 64
)

type ColumnSchema struct {
	Name string
	Type DataType
}

var DefaultSchema = []ColumnSchema{
	{"Trade_ID", TypeInt},
	{"Symbol", TypeString},
	{"Price", TypeDouble},
	{"Quantity", TypeDouble},
	{"Is_Valid", TypeBool},
}

func TypeSize(t DataType) int {
	switch t {
	case TypeInt:
		return 4
	case TypeDouble:
		return 8
	case TypeBool:
		return 1
	case TypeString:
		return StringWidth
	}
	return 0
}
