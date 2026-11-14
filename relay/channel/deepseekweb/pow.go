package deepseekweb

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync"
)

// The chat.deepseek.com web client gates every chat completion and file
// upload behind a proof-of-work challenge. The algorithm ("DeepSeekHashV1")
// is a reduced Keccak-f[1600] sponge: rate 136 bytes, SHA-3 padding
// (0x06 ... 0x80) and exactly 23 rounds using the standard round constants
// RC[1..23] (RC[0] is skipped). The digest is the first 32 bytes of the
// state, little-endian per lane.
//
// The challenge message is "{salt}_{expire_at}_{nonce}" with a decimal nonce
// starting at 0; a solution is any nonce whose digest equals the challenge.

// powMessage builds the challenge message for a nonce.
func powMessage(salt string, expireAt int64, nonce uint64) []byte {
	msg := make([]byte, 0, len(salt)+32)
	msg = append(msg, salt...)
	msg = append(msg, '_')
	msg = strconv.AppendInt(msg, expireAt, 10)
	msg = append(msg, '_')
	msg = strconv.AppendUint(msg, nonce, 10)
	return msg
}

// deepSeekHashV1 hashes msg with the 23-round Keccak variant described above.
func deepSeekHashV1(msg []byte) [32]byte {
	var st [25]uint64
	keccakAbsorb(&st, msg)
	var out [32]byte
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint64(out[i*8:], st[i])
	}
	return out
}

// keccakAbsorb applies the sponge absorb phase with SHA-3 padding.
func keccakAbsorb(st *[25]uint64, msg []byte) {
	const rate = 136 // 1600/8 - 2*256/8 capacity for a 256-bit digest
	for len(msg) >= rate {
		keccakXorBlock(st, msg[:rate])
		keccakF23(st)
		msg = msg[rate:]
	}
	var blk [rate]byte
	n := copy(blk[:], msg)
	blk[n] = 0x06 // SHA-3 domain separator
	blk[rate-1] |= 0x80
	keccakXorBlock(st, blk[:])
	keccakF23(st)
}

func keccakXorBlock(st *[25]uint64, block []byte) {
	// A rate block carries 136/8 = 17 lanes; never read past its length.
	for i := 0; i < len(block)/8; i++ {
		st[i] ^= binary.LittleEndian.Uint64(block[i*8:])
	}
}

var keccakRC = [24]uint64{
	0x0000000000000001, 0x0000000000008082, 0x800000000000808A, 0x8000000080008000,
	0x000000000000808B, 0x0000000080000001, 0x8000000080008081, 0x8000000000008009,
	0x000000000000008A, 0x0000000000000088, 0x0000000080008009, 0x000000008000000A,
	0x000000008000808B, 0x800000000000008B, 0x8000000000008089, 0x8000000000008003,
	0x8000000000008002, 0x8000000000000080, 0x000000000000800A, 0x800000008000000A,
	0x8000000080008081, 0x8000000000008080, 0x0000000080000001, 0x8000000080008008,
}

var keccakRotc = [24]int{1, 3, 6, 10, 15, 21, 28, 36, 45, 55, 2, 14, 27, 41, 56, 8, 25, 43, 62, 18, 39, 61, 20, 44}
var keccakPiln = [24]int{10, 7, 11, 17, 18, 3, 5, 16, 8, 21, 24, 4, 15, 23, 19, 13, 12, 2, 20, 14, 22, 9, 6, 1}

func keccakRotl64(x uint64, n int) uint64 {
	n &= 63
	if n == 0 {
		return x
	}
	return x<<uint(n) | x>>uint(64-n)
}

// keccakF23 runs rounds 1..23 of the Keccak-f[1600] permutation, skipping
// RC[0]. Theta/rho/pi/chi steps match the standard permutation.
func keccakF23(a *[25]uint64) {
	var bc [5]uint64
	for round := 1; round <= 23; round++ {
		for i := 0; i < 5; i++ {
			bc[i] = a[i] ^ a[i+5] ^ a[i+10] ^ a[i+15] ^ a[i+20]
		}
		for i := 0; i < 5; i++ {
			t := bc[(i+4)%5] ^ keccakRotl64(bc[(i+1)%5], 1)
			for j := 0; j < 25; j += 5 {
				a[j+i] ^= t
			}
		}
		t := a[1]
		for i := 0; i < 24; i++ {
			j := keccakPiln[i]
			tmp := a[j]
			a[j] = keccakRotl64(t, keccakRotc[i])
			t = tmp
		}
		for j := 0; j < 25; j += 5 {
			for i := 0; i < 5; i++ {
				bc[i] = a[j+i]
			}
			for i := 0; i < 5; i++ {
				a[j+i] = bc[i] ^ (^bc[(i+1)%5] & bc[(i+2)%5])
			}
		}
		a[0] ^= keccakRC[round]
	}
}

// PowChallenge mirrors data.biz_data.challenge of /chat/create_pow_challenge.
type PowChallenge struct {
	Algorithm   string `json:"algorithm"`
	Challenge   string `json:"challenge"`
	Salt        string `json:"salt"`
	Signature   string `json:"signature"`
	Difficulty  int64  `json:"difficulty"`
	ExpireAt    int64  `json:"expire_at"`
	ExpireAfter int64  `json:"expire_after"`
	TargetPath  string `json:"target_path"`
}

const powAlgorithmDeepSeekHashV1 = "DeepSeekHashV1"

// powScanLimit caps the brute-force scan so a changed upstream algorithm
// (or a corrupted challenge) fails fast instead of burning CPU forever.
// Observed difficulty (~144000) is far below it.
const powScanLimit = 1 << 32

// Solve scans nonces until the DeepSeekHashV1 digest matches the challenge.
// Workers scan interleaved ranges (worker w tries w, w+N, w+2N, ...), so the
// first hit wins regardless of which range it lands in. Expected work is
// ~Difficulty digests; at the observed difficulty (~144000) this completes
// in well under a second on modern hardware.
func (c *PowChallenge) Solve() (uint64, error) {
	if c.Algorithm != "" && c.Algorithm != powAlgorithmDeepSeekHashV1 {
		return 0, fmt.Errorf("unsupported pow algorithm: %s", c.Algorithm)
	}
	target, err := hex.DecodeString(c.Challenge)
	if err != nil {
		return 0, fmt.Errorf("bad challenge hex: %w", err)
	}
	if len(target) != 32 {
		return 0, errors.New("challenge digest must be 32 bytes")
	}
	var want [32]byte
	copy(want[:], target)

	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}
	perWorker := powScanLimit / uint64(workers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	found := make(chan uint64, 1)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			for offset := uint64(0); offset < perWorker; offset++ {
				nonce := seed + offset*uint64(workers)
				select {
				case <-ctx.Done():
					return
				default:
				}
				if deepSeekHashV1(powMessage(c.Salt, c.ExpireAt, nonce)) == want {
					select {
					case found <- nonce:
					default:
					}
					cancel()
					return
				}
			}
		}(uint64(w))
	}
	wg.Wait()
	select {
	case nonce := <-found:
		return nonce, nil
	default:
		return 0, errors.New("pow: no answer found within scan limit")
	}
}

// PowResponseHeader renders the x-ds-pow-response header value: the
// challenge answer as base64(JSON).
func PowResponseHeader(ch *PowChallenge, answer uint64) (string, error) {
	raw, err := json.Marshal(struct {
		Algorithm  string `json:"algorithm"`
		Challenge  string `json:"challenge"`
		Salt       string `json:"salt"`
		Answer     uint64 `json:"answer"`
		Signature  string `json:"signature"`
		TargetPath string `json:"target_path"`
	}{
		Algorithm:  ch.Algorithm,
		Challenge:  ch.Challenge,
		Salt:       ch.Salt,
		Answer:     answer,
		Signature:  ch.Signature,
		TargetPath: ch.TargetPath,
	})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// solveChallenge fetches no network; it is split from the header rendering so
// callers can log the two steps separately.
func solveChallenge(ch *PowChallenge) (uint64, string, error) {
	answer, err := ch.Solve()
	if err != nil {
		return 0, "", err
	}
	header, err := PowResponseHeader(ch, answer)
	if err != nil {
		return 0, "", err
	}
	return answer, header, nil
}
