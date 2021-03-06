package anyrnn

import (
	"fmt"

	"github.com/unixpickle/anydiff"
	"github.com/unixpickle/anynet"
	"github.com/unixpickle/anyvec"
	"github.com/unixpickle/essentials"
	"github.com/unixpickle/serializer"
)

func init() {
	var s Stack
	serializer.RegisterTypedDeserializer(s.SerializerType(), DeserializeStack)
}

// A Stack is a meta-Block for composing Blocks.
// In a Stack, the first Block's output is fed as input to
// the next Block, etc.
//
// An empty Stack is invalid.
type Stack []Block

// DeserializeStack deserializes a Stack.
func DeserializeStack(d []byte) (Stack, error) {
	blockSlice, err := serializer.DeserializeSlice(d)
	if err != nil {
		return nil, essentials.AddCtx("deserialize Stack", err)
	}
	res := make(Stack, len(blockSlice))
	for i, x := range blockSlice {
		if b, ok := x.(Block); ok {
			res[i] = b
		} else {
			return nil, fmt.Errorf("deserialize Stack: type is not a Block: %T", x)
		}
	}
	return res, nil
}

// Start produces a start StackState.
func (s Stack) Start(n int) State {
	s.assertNonEmpty()
	res := make(StackState, len(s))
	for i, x := range s {
		res[i] = x.Start(n)
	}
	return res
}

// PropagateStart back-propagates through the start state.
func (s Stack) PropagateStart(sg StateGrad, g anydiff.Grad) {
	for i, x := range s {
		x.PropagateStart(sg.(StackGrad)[i], g)
	}
}

// Step applies the block for a single timestep.
func (s Stack) Step(st State, in anyvec.Vector) Res {
	res := &stackRes{V: anydiff.VarSet{}}
	inVec := in
	for i, x := range s {
		inState := st.(StackState)[i]
		blockRes := x.Step(inState, inVec)
		inVec = blockRes.Output()
		res.Reses = append(res.Reses, blockRes)
		res.OutState = append(res.OutState, blockRes.State())
		res.V = anydiff.MergeVarSets(res.V, blockRes.Vars())
	}
	return res
}

// Parameters gathers the parameters of all the sub-blocks
// that implement anynet.Parameterizer.
func (s Stack) Parameters() []*anydiff.Var {
	var res []*anydiff.Var
	for _, x := range s {
		if p, ok := x.(anynet.Parameterizer); ok {
			res = append(res, p.Parameters()...)
		}
	}
	return res
}

// SerializerType returns the unique ID used to serialize
// a Stack with the serializer package.
func (s Stack) SerializerType() string {
	return "github.com/unixpickle/anynet/anyrnn.Stack"
}

// Serialize serializes the Stack.
// It only works if every child is a Serializer.
func (s Stack) Serialize() ([]byte, error) {
	var res []serializer.Serializer
	for _, x := range s {
		if ser, ok := x.(serializer.Serializer); ok {
			res = append(res, ser)
		} else {
			return nil, fmt.Errorf("not a serializer: %T", x)
		}
	}
	return serializer.SerializeSlice(res)
}

func (s Stack) assertNonEmpty() {
	if len(s) == 0 {
		panic("empty Stack is invalid")
	}
}

type stackRes struct {
	Reses    []Res
	OutState StackState
	V        anydiff.VarSet
}

func (s *stackRes) State() State {
	return s.OutState
}

func (s *stackRes) Output() anyvec.Vector {
	return s.Reses[len(s.Reses)-1].Output()
}

func (s *stackRes) Vars() anydiff.VarSet {
	return s.V
}

func (s *stackRes) Propagate(u anyvec.Vector, sg StateGrad, g anydiff.Grad) (anyvec.Vector,
	StateGrad) {
	downVec := u
	downStates := make(StackGrad, len(s.Reses))
	for i := len(s.Reses) - 1; i >= 0; i-- {
		var stateUpstream StateGrad
		if sg != nil {
			stateUpstream = sg.(StackGrad)[i]
		}
		down, downState := s.Reses[i].Propagate(downVec, stateUpstream, g)
		downVec = down
		downStates[i] = downState
	}
	return downVec, downStates
}

// StackState is the State type for a Stack.
//
// Each State in the slice corresponds to a Block in the
// Stack.
type StackState []State

// Present returns the present map of one of the internal
// states.
func (s StackState) Present() PresentMap {
	return s[0].Present()
}

// Reduce reduces all the internal states.
func (s StackState) Reduce(p PresentMap) State {
	res := make(StackState, len(s))
	for i, x := range s {
		res[i] = x.Reduce(p)
	}
	return res
}

// StackGrad is the StateGrad type for a Stack.
//
// It is pretty much analogous to StackState.
type StackGrad []StateGrad

// Present returns the present map of one of the internal
// state grads.
func (s StackGrad) Present() PresentMap {
	return s[0].Present()
}

// Expand expands all the internal state grads.
func (s StackGrad) Expand(p PresentMap) StateGrad {
	res := make(StackGrad, len(s))
	for i, x := range s {
		res[i] = x.Expand(p)
	}
	return res
}
