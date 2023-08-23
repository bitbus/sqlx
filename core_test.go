package sqlx

import (
	"database/sql"
	"reflect"
	"testing"
)

func TestQueryable(t *testing.T) {
	sqlDBType := reflect.TypeOf(&sql.DB{})
	dbType := reflect.TypeOf(&DB{})
	sqlTxType := reflect.TypeOf(&sql.Tx{})
	txType := reflect.TypeOf(&Tx{})

	dbMethods := exportableMethods(sqlDBType)
	for k, v := range exportableMethods(dbType) {
		dbMethods[k] = v
	}

	txMethods := exportableMethods(sqlTxType)
	for k, v := range exportableMethods(txType) {
		txMethods[k] = v
	}

	sharedMethods := make([]string, 0)

	for name, dbMethod := range dbMethods {
		if txMethod, ok := txMethods[name]; ok {
			if methodsEqual(dbMethod.Type, txMethod.Type) {
				sharedMethods = append(sharedMethods, name)
			}
		}
	}

	queryableType := reflect.TypeOf((*Queryable)(nil)).Elem()
	queryableMethods := exportableMethods(queryableType)

	for _, sharedMethodName := range sharedMethods {
		if _, ok := queryableMethods[sharedMethodName]; !ok {
			t.Errorf("Queryable does not include shared DB/Tx method: %s", sharedMethodName)
		}
	}
}

func exportableMethods(t reflect.Type) map[string]reflect.Method {
	methods := make(map[string]reflect.Method)

	for i := 0; i < t.NumMethod(); i++ {
		method := t.Method(i)

		if method.IsExported() {
			methods[method.Name] = method
		}
	}

	return methods
}

func methodsEqual(t reflect.Type, ot reflect.Type) bool {
	if t.NumIn() != ot.NumIn() || t.NumOut() != ot.NumOut() || t.IsVariadic() != ot.IsVariadic() {
		return false
	}

	// Start at 1 to avoid comparing receiver argument
	for i := 1; i < t.NumIn(); i++ {
		if t.In(i) != ot.In(i) {
			return false
		}
	}

	for i := 0; i < t.NumOut(); i++ {
		if t.Out(i) != ot.Out(i) {
			return false
		}
	}

	return true
}
