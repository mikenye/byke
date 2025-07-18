package byke

import (
	"fmt"
	"github.com/oliverbestmann/byke/internal/refl"
	"github.com/oliverbestmann/byke/internal/typedpool"
	"reflect"
)

var valueSlices = typedpool.New[[]reflect.Value]()

type systemTrigger struct {
	TargetId   EntityId
	EventValue any
}

type systemContext struct {
	// a value that has triggerd the execution of the system.
	// Should be an event.
	Trigger systemTrigger

	// last tick the system ran
	LastRun Tick
}

func (w *World) prepareSystemUncached(config systemConfig) *preparedSystem {
	rSystem := config.SystemFunc

	if rSystem.Kind() != reflect.Func {
		panic(fmt.Sprintf("not a function: %s", rSystem.Type()))
	}

	preparedSystem := &preparedSystem{systemConfig: config}

	systemType := rSystem.Type()

	// collect a number of functions that when called will prepare the systems parameters
	var params []SystemParamState

	for idx := range systemType.NumIn() {
		inType := systemType.In(idx)

		resourceCopy, resourceCopyOk := w.resources[reflect.PointerTo(inType)]
		resource, resourceOk := w.resources[inType]

		switch {
		case refl.ImplementsInterfaceDirectly[SystemParam](inType):
			params = append(params, makeSystemParamState(w, inType))

		case refl.ImplementsInterfaceDirectly[SystemParam](reflect.PointerTo(inType)):
			params = append(params, makeSystemParamState(w, inType))

		case inType == reflect.TypeFor[*World]():
			params = append(params, valueSystemParamState(reflect.ValueOf(w)))

		case resourceCopyOk:
			params = append(params, valueSystemParamState(resourceCopy.Reflect.Elem()))

		case resourceOk:
			params = append(params, valueSystemParamState(resource.Reflect.Value))

		default:
			panic(fmt.Sprintf("Can not handle system param of type %s", inType))
		}
	}

	// verify that all the param types match their actual types
	for idx, param := range params {
		inType := systemType.In(idx)
		if !param.valueType().AssignableTo(inType) {
			panic(fmt.Sprintf("Argument %d of %s is not assignable to param value of type %s", idx, systemType.Name(), inType))
		}
	}

	// check the return values. we currently only allow a `bool` return value
	if systemType.NumOut() > 0 {
		if systemType.NumOut() > 1 {
			panic("System must have at most one return value")
		}

		returnType := systemType.Out(0)
		if returnType != reflect.TypeFor[bool]() {
			panic("for now, only bool is accepted as a return type of a system")
		}

		preparedSystem.IsPredicate = true
	}

	preparedSystem.RawSystem = func(sc systemContext) any {
		paramValues := valueSlices.Get()
		defer valueSlices.Put(paramValues)

		*paramValues = (*paramValues)[:0]

		sc.LastRun = preparedSystem.LastRun

		for _, param := range params {
			*paramValues = append(*paramValues, param.getValue(sc))
		}

		returnValues := rSystem.Call(*paramValues)

		for idx, param := range params {
			param.cleanupValue((*paramValues)[idx])
		}

		// clear any pointers that are still in the param slice
		clear(*paramValues)

		// convert return value to interface
		var returnValue any
		if len(returnValues) == 1 {
			returnValue = returnValues[0].Interface()
		}

		return returnValue
	}

	// prepare predicate systems if any
	for _, predicate := range config.Predicates {
		for _, system := range asSystemConfigs(predicate) {
			predicateSystem := w.prepareSystem(system)
			if !predicateSystem.IsPredicate {
				panic("predicate system is not actually a predicate")
			}

			preparedSystem.Predicates = append(preparedSystem.Predicates, predicateSystem)
		}
	}

	return preparedSystem
}

func makeSystemParamState(world *World, ty reflect.Type) SystemParamState {
	for ty.Kind() == reflect.Pointer {
		ty = ty.Elem()
	}

	// allocate a new instance on the heap and get the value as an interface
	param := reflect.New(ty).Interface().(SystemParam)

	// initialize using the world
	return param.init(world)
}
