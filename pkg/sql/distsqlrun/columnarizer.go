// Copyright 2018 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.

package distsqlrun

import (
	"fmt"

	"github.com/cockroachdb/cockroach/pkg/sql/exec"
	"github.com/cockroachdb/cockroach/pkg/sql/exec/types"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/sqlbase"
	"github.com/cockroachdb/cockroach/pkg/util/encoding"
)

// columnarizer turns a RowSource input into an exec.Operator output, by reading
// the input in chunks of size exec.ColBatchSize and converting each chunk into
// an exec.ColBatch column by column.
type columnarizer struct {
	ProcessorBase

	input RowSource
	da    sqlbase.DatumAlloc

	buffered sqlbase.EncDatumRows
	batch    exec.ColBatch
}

// newColumnarizer returns a new columnarizer
func newColumnarizer(flowCtx *FlowCtx, processorID int32, input RowSource) (*columnarizer, error) {
	c := &columnarizer{
		input: input,
	}
	if err := c.ProcessorBase.Init(
		nil,
		&PostProcessSpec{},
		input.OutputTypes(),
		flowCtx,
		processorID,
		nil,
		nil, /* memMonitor */
		ProcStateOpts{InputsToDrain: []RowSource{input}},
	); err != nil {
		return nil, err
	}
	c.Init()
	return c, nil
}

func (c *columnarizer) Init() {
	outputTypes := c.OutputTypes()
	typs := make([]types.T, len(outputTypes))
	for i := range typs {
		typs[i] = types.FromColumnType(outputTypes[i])
	}
	c.batch = exec.NewMemBatch(typs...)
	c.buffered = make(sqlbase.EncDatumRows, exec.ColBatchSize)
	for i := range c.buffered {
		c.buffered[i] = make(sqlbase.EncDatumRow, len(typs))
	}
}

func (c *columnarizer) Next() exec.ColBatch {
	// Buffer up n rows.
	nRows := uint16(0)
	columnTypes := c.OutputTypes()
	for ; nRows < exec.ColBatchSize; nRows++ {
		row, meta := c.input.Next()
		if meta != nil {
			panic("TODO(jordan): columnarizer needs to forward metadata.")
		}
		if row == nil {
			break
		}
		// TODO(jordan): evaluate whether it's more efficient to skip the buffer
		// phase.
		copy(c.buffered[nRows], row)
	}
	c.batch.SetLength(nRows)

	// Write each column into the output batch.
	for idx, ct := range columnTypes {
		vec := c.batch.ColVec(idx)
		switch ct.SemanticType {
		// TODO(solon): these should be autogenerated from a template.
		case sqlbase.ColumnType_BOOL:
			col := vec.Bool()
			for i := uint16(0); i < nRows; i++ {
				ed := c.buffered[i][idx]
				if err := ed.EnsureDecoded(&ct, &c.da); err != nil {
					panic(err)
				}
				if ed.Datum == tree.DNull {
					vec.SetNull(i)
				}
				col.Set(i, bool(*ed.Datum.(*tree.DBool)))
			}
		case sqlbase.ColumnType_INT:
			switch ct.Width {
			case 8:
				col := vec.Int8()
				for i := uint16(0); i < nRows; i++ {
					ed := c.buffered[i][idx]
					if err := ed.EnsureDecoded(&ct, &c.da); err != nil {
						panic(err)
					}
					if ed.Datum == tree.DNull {
						vec.SetNull(i)
					}
					col[i] = int8(*ed.Datum.(*tree.DInt))
				}
			case 16:
				col := vec.Int16()
				for i := uint16(0); i < nRows; i++ {
					ed := c.buffered[i][idx]
					if err := ed.EnsureDecoded(&ct, &c.da); err != nil {
						panic(err)
					}
					if ed.Datum == tree.DNull {
						vec.SetNull(i)
					}
					col[i] = int16(*ed.Datum.(*tree.DInt))
				}
			case 32:
				col := vec.Int32()
				for i := uint16(0); i < nRows; i++ {
					ed := c.buffered[i][idx]
					if err := ed.EnsureDecoded(&ct, &c.da); err != nil {
						panic(err)
					}
					if ed.Datum == tree.DNull {
						vec.SetNull(i)
					}
					col[i] = int32(*ed.Datum.(*tree.DInt))
				}
			case 0, 64:
				col := vec.Int64()
				for i := uint16(0); i < nRows; i++ {
					if c.buffered[i][idx].Datum == nil {
						if err := c.buffered[i][idx].EnsureDecoded(&ct, &c.da); err != nil {
							panic(err)
						}
					}
					if c.buffered[i][idx].Datum == tree.DNull {
						vec.SetNull(i)
					}
					col[i] = int64(*c.buffered[i][idx].Datum.(*tree.DInt))
				}
			default:
				panic(fmt.Sprintf("integer with unknown width %d", ct.Width))
			}
		case sqlbase.ColumnType_FLOAT:
			col := vec.Float64()
			for i := uint16(0); i < nRows; i++ {
				ed := c.buffered[i][idx]
				if err := ed.EnsureDecoded(&ct, &c.da); err != nil {
					panic(err)
				}
				if ed.Datum == tree.DNull {
					vec.SetNull(i)
				}
				col[i] = float64(*ed.Datum.(*tree.DFloat))
			}
		case sqlbase.ColumnType_BYTES:
			col := vec.Bytes()
			for i := uint16(0); i < nRows; i++ {
				if c.buffered[i][idx].Datum == nil {
					if err := c.buffered[i][idx].EnsureDecoded(&ct, &c.da); err != nil {
						panic(err)
					}
				}
				if c.buffered[i][idx].Datum == tree.DNull {
					vec.SetNull(i)
				}
				col.Set(i, encoding.UnsafeConvertStringToBytes(string(*c.buffered[i][idx].Datum.(*tree.DBytes))))
			}
		case sqlbase.ColumnType_STRING:
			col := vec.Bytes()
			for i := uint16(0); i < nRows; i++ {
				if c.buffered[i][idx].Datum == nil {
					if err := c.buffered[i][idx].EnsureDecoded(&ct, &c.da); err != nil {
						panic(err)
					}
				}
				if c.buffered[i][idx].Datum == tree.DNull {
					vec.SetNull(i)
				}
				col.Set(i, encoding.UnsafeConvertStringToBytes(string(*c.buffered[i][idx].Datum.(*tree.DString))))
			}
		default:
			panic(fmt.Sprintf("Unsupported column type %s", ct.SQLString()))
		}
	}
	return c.batch
}

var _ exec.Operator = &columnarizer{}
