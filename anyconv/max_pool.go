package anyconv

import (
	"sync"

	"github.com/unixpickle/anydiff"
	"github.com/unixpickle/anyvec"
	"github.com/unixpickle/essentials"
	"github.com/unixpickle/serializer"
)

func init() {
	var m MaxPool
	serializer.RegisterTypedDeserializer(m.SerializerType(), DeserializeMaxPool)
}

// MaxPool is a max-pooling layer.
//
// All input and output tensors are row-major depth-minor.
//
// If the span along a dimension doesn't divide the
// corresponding input dimension, then any input values in
// an "incomplete" pool are ignored.
type MaxPool struct {
	// Span is equivalent to a convolutional layer's filter
	// size.
	SpanX int
	SpanY int

	// Stride is equivalent to a convolutional layer's
	// stride.
	StrideX int
	StrideY int

	InputWidth  int
	InputHeight int
	InputDepth  int

	im2colLock sync.Mutex
	im2col     anyvec.Mapper
}

// DeserializeMaxPool deserializes a MaxPool.
func DeserializeMaxPool(d []byte) (*MaxPool, error) {
	var sX, sY, iW, iH, iD, strideX, strideY serializer.Int
	err := serializer.DeserializeAny(d, &sX, &sY, &iW, &iH, &iD, &strideX, &strideY)
	if err != nil {
		// Legacy format did not store strideX and strideY.
		err = serializer.DeserializeAny(d, &sX, &sY, &iW, &iH, &iD)
		if err != nil {
			return nil, essentials.AddCtx("deserialize MaxPool", err)
		}
		strideX = sX
		strideY = sY
	}
	return &MaxPool{
		SpanX:       int(sX),
		SpanY:       int(sY),
		StrideX:     int(strideX),
		StrideY:     int(strideY),
		InputWidth:  int(iW),
		InputHeight: int(iH),
		InputDepth:  int(iD),
	}, nil
}

// OutputWidth returns the width of the output tensor.
func (m *MaxPool) OutputWidth() int {
	return m.surrogateConv().OutputWidth()
}

// OutputHeight returns the height of the output tensor.
func (m *MaxPool) OutputHeight() int {
	return m.surrogateConv().OutputHeight()
}

// OutputDepth returns the depth of the output tensor.
func (m *MaxPool) OutputDepth() int {
	return m.InputDepth
}

// Apply applies the layer to an input tensor.
func (m *MaxPool) Apply(in anydiff.Res, batchSize int) anydiff.Res {
	m.im2colLock.Lock()
	if m.im2col == nil {
		m.initIm2Col(in.Output().Creator())
	}
	m.im2colLock.Unlock()

	imgSize := m.InputWidth * m.InputHeight * m.InputDepth
	if in.Output().Len() != batchSize*imgSize {
		panic("incorrect input size")
	}

	im2ColTemp := in.Output().Creator().MakeVector(m.im2col.OutSize())

	var maxResults []anyvec.Vector
	var maxMaps []anyvec.Mapper
	for i := 0; i < batchSize; i++ {
		subIn := in.Output().Slice(imgSize*i, imgSize*(i+1))
		m.im2col.Map(subIn, im2ColTemp)
		mapping := anyvec.MapMax(im2ColTemp, m.SpanX*m.SpanY)
		output := in.Output().Creator().MakeVector(mapping.OutSize())
		mapping.Map(im2ColTemp, output)
		maxMaps = append(maxMaps, mapping)
		maxResults = append(maxResults, output)
	}

	return &maxPoolRes{
		Layer:  m,
		In:     in,
		OutVec: in.Output().Creator().Concat(maxResults...),
		Maps:   maxMaps,
	}
}

// SerializerType returns the unique ID used to serialize
// a MaxPool with the serializer package.
func (m *MaxPool) SerializerType() string {
	return "github.com/unixpickle/anynet/anyconv.MaxPool"
}

// Serialize serializes the MaxPool.
func (m *MaxPool) Serialize() ([]byte, error) {
	return serializer.SerializeAny(
		serializer.Int(m.SpanX),
		serializer.Int(m.SpanY),
		serializer.Int(m.InputWidth),
		serializer.Int(m.InputHeight),
		serializer.Int(m.InputDepth),
		serializer.Int(m.StrideX),
		serializer.Int(m.StrideY),
	)
}

func (m *MaxPool) initIm2Col(cr anyvec.Creator) {
	var mapping []int

	for y := 0; y+m.SpanY <= m.InputHeight; y += m.StrideY {
		for x := 0; x+m.SpanX <= m.InputWidth; x += m.StrideX {
			for subZ := 0; subZ < m.InputDepth; subZ++ {
				for subY := 0; subY < m.SpanY; subY++ {
					subYIdx := (y + subY) * m.InputWidth * m.InputDepth
					for subX := 0; subX < m.SpanX; subX++ {
						subXIdx := subYIdx + (subX+x)*m.InputDepth
						mapping = append(mapping, subXIdx+subZ)
					}
				}
			}
		}
	}

	inSize := m.InputWidth * m.InputHeight * m.InputDepth
	m.im2col = cr.MakeMapper(inSize, mapping)
}

func (m *MaxPool) surrogateConv() *Conv {
	return &Conv{
		FilterCount:  m.InputDepth,
		FilterWidth:  m.SpanX,
		FilterHeight: m.SpanY,
		StrideX:      m.StrideX,
		StrideY:      m.StrideY,
		InputWidth:   m.InputWidth,
		InputHeight:  m.InputHeight,
		InputDepth:   m.InputDepth,
	}
}

type maxPoolRes struct {
	Layer  *MaxPool
	In     anydiff.Res
	OutVec anyvec.Vector
	Maps   []anyvec.Mapper
}

func (m *maxPoolRes) Output() anyvec.Vector {
	return m.OutVec
}

func (m *maxPoolRes) Vars() anydiff.VarSet {
	return m.In.Vars()
}

func (m *maxPoolRes) Propagate(u anyvec.Vector, g anydiff.Grad) {
	outSize := u.Len() / len(m.Maps)
	var upPieces []anyvec.Vector
	for i, mapper := range m.Maps {
		upSlice := u.Slice(outSize*i, outSize*(i+1))
		permed := u.Creator().MakeVector(mapper.InSize())
		mapper.MapTranspose(upSlice, permed)
		upPiece := u.Creator().MakeVector(m.Layer.im2col.InSize())
		m.Layer.im2col.MapTranspose(permed, upPiece)
		upPieces = append(upPieces, upPiece)
	}
	upstream := u.Creator().Concat(upPieces...)
	m.In.Propagate(upstream, g)
}
