package rice

// method indexes App's per-verb route trees.
//
// Dispatch on a common verb is therefore an array index, not a map lookup and
// not a string comparison. See docs/02-architecture.md.
type method uint8

const (
	mGET method = iota
	mPOST
	mPUT
	mPATCH
	mDELETE
	mHEAD
	mOPTIONS

	// methodCount is the size of App's trees array. It must stay last.
	methodCount
)

// methodIndex maps an HTTP verb to its tree index, reporting false for verbs
// that do not have a reserved slot.
//
// It switches on length first so that most verbs are separated before any byte
// is compared. Each comparison is written as string(m) == "LITERAL", which the
// compiler evaluates without copying the bytes, so the whole function allocates
// nothing. TestAllocBudgetMethodIndex pins that down rather than assuming it.
func methodIndex(m []byte) (method, bool) {
	switch len(m) {
	case 3:
		if string(m) == "GET" {
			return mGET, true
		}
		if string(m) == "PUT" {
			return mPUT, true
		}
	case 4:
		if string(m) == "POST" {
			return mPOST, true
		}
		if string(m) == "HEAD" {
			return mHEAD, true
		}
	case 5:
		if string(m) == "PATCH" {
			return mPATCH, true
		}
	case 6:
		if string(m) == "DELETE" {
			return mDELETE, true
		}
	case 7:
		if string(m) == "OPTIONS" {
			return mOPTIONS, true
		}
	}
	return 0, false
}
