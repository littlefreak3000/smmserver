package webui

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf16"
)

// Course is what the level viewer draws: the header of one area of a Super Mario Maker course and its objects. The
// layout of the (decompressed, 0x15000 byte) course blob is the one HerobrineTV's SMM1-Level-Downloader (MIT) reads:
// all numbers big endian, a header up to 0xF0, then up to 2600 objects of 32 bytes.
type Course struct {
	Name        string   `json:"name"`
	Mode        string   `json:"mode"` // M1, M3, MW or WU: the game style
	Theme       int      `json:"theme"`
	ThemeName   string   `json:"themeName"`
	TimeLimit   int      `json:"timeLimit"`
	Scroll      int      `json:"scroll"`
	ScrollName  string   `json:"scrollName"`
	WidthBlocks int      `json:"widthBlocks"`
	Objects     []Object `json:"objects"`
}

// Object is one placed thing. X, Y and Z are in tiles; Size is 2 for a "big" (double size) enemy or item.
type Object struct {
	X          int    `json:"x"`
	Y          int    `json:"y"`
	Z          int    `json:"z"`
	Width      int    `json:"w"`
	Height     int    `json:"h"`
	Flags      uint32 `json:"flags"`
	ChildFlags uint32 `json:"childFlags"`
	Extended   uint32 `json:"ext"`
	Type       int    `json:"type"`
	ChildType  int    `json:"childType"`
	LinkID     int    `json:"link"`
	Effect     int    `json:"effect"`
	Transform  int    `json:"transform"`
	ChildXform int    `json:"childTransform"`
	Size       int    `json:"size"`
	SubType    int    `json:"sub"`
	Wing       int    `json:"wing"`
	Block      bool   `json:"block"`
	Name       string `json:"label"`
}

const (
	headerSize = 0xF0
	objectSize = 32
	maxObjects = 2600
)

var themeNames = map[int]string{0: "overworld", 1: "underground", 2: "castle", 3: "airship", 4: "water", 5: "ghostHouse"}
var scrollNames = map[int]string{0: "none", 1: "slow", 2: "medium", 3: "fast"}

// blockNames are the object types that are terrain-like (drawn from the tile sheet); the rest are enemies and items.
var blockNames = map[int]string{
	4: "Brick Block", 5: "? Block", 6: "Hard Block", 7: "Ground", 8: "Coin", 9: "Pipe", 14: "Mushroom Terrain",
	16: "Box Terrain", 17: "Bridge", 21: "Donut Block", 22: "Cloud Block", 23: "Note Block", 26: "Goal Ground",
	29: "Clear Block", 37: "Start Ground", 43: "Spike Block", 49: "Castle Bridge", 53: "Conveyor", 59: "Track",
	63: "Ice Block", 64: "Vine Block",
}

var monsterNames = map[int]string{
	0: "Goomba", 1: "Koopa Troopa", 2: "Piranha Plant", 3: "Hammer Bro", 10: "Jump Platform", 11: "Lift", 12: "Thwomp",
	13: "Bullet Bill Cannon", 15: "Bob-Omb", 18: "P Switch", 19: "POW Block", 20: "Mushroom", 24: "Fire Bar", 25: "Spiny",
	27: "Goal Pole", 28: "Buzzy Beetle", 30: "Lakitu", 31: "Lakitu Cloud", 32: "Vine Head", 33: "1-Up", 34: "Fire Flower",
	35: "Super Star", 36: "Lava Lift", 38: "Start Sign", 39: "Magikoopa", 40: "Spike Top", 41: "Boo", 42: "Clown Car",
	44: "Costume Mushroom", 45: "Shoe", 46: "Dry Bones", 47: "Cannon", 48: "Blooper", 50: "Weird Mushroom",
	51: "Mega Mushroom", 52: "Wiggler", 54: "Burner", 55: "Door", 56: "Cheep Cheep", 57: "Muncher", 58: "Rocky Wrench",
	60: "Podoboo", 61: "Chain Chomp", 62: "Bowser", 65: "Player", 66: "Airship Sign", 67: "One-way Gate", 68: "Grinder",
	69: "Player",
}

// ParseCourse reads one decompressed course blob.
func ParseCourse(data []byte) (*Course, error) {
	if len(data) < headerSize {
		return nil, errors.New("the course data is too small")
	}

	u16 := func(p int) int { return int(binary.BigEndian.Uint16(data[p:])) }
	u32 := func(p int) uint32 { return binary.BigEndian.Uint32(data[p:]) }
	s8 := func(p int) int { return int(int8(data[p])) }
	s16 := func(p int) int { return int(int16(binary.BigEndian.Uint16(data[p:]))) }

	count := int(int32(u32(0xEC)))
	if count < 0 || count > maxObjects {
		return nil, fmt.Errorf("corrupted course: object count %d", count)
	}

	theme, scroll := int(data[0x6D]), int(data[0x72])

	name := func(p, n int) string {
		units := make([]uint16, 0, n/2)
		for i := 0; i+1 < n; i += 2 {
			v := binary.BigEndian.Uint16(data[p+i:])
			if v == 0 {
				break
			}

			units = append(units, v)
		}

		return string(utf16.Decode(units))
	}

	mode := string(trimNul(data[0x6A : 0x6A+2]))

	c := &Course{
		Name:        name(0x28, 66),
		Mode:        mode,
		Theme:       theme,
		ThemeName:   themeNames[theme],
		TimeLimit:   u16(0x70),
		Scroll:      scroll,
		ScrollName:  scrollNames[scroll],
		WidthBlocks: int(int32(u32(0x74))) / 16,
		Objects:     make([]Object, 0, count),
	}

	if c.ThemeName == "" {
		c.ThemeName = "overworld"
	}

	for i := 0; i < count; i++ {
		p := headerSize + objectSize*i
		if p+objectSize > len(data) {
			break
		}

		typ := s8(p + 0x18)
		flags := u32(p + 0x0C)
		_, isBlock := blockNames[typ]

		sub := int((flags & 7 & 4) >> 2)
		if isBlock {
			sub = int((flags & 7) >> 2)
		}

		size := 1
		if !isBlock && typ != 3 {
			size = int((flags&0x4000)>>14) + 1
		}

		addY := 0
		if typ == 30 {
			addY = 1
		}

		label := blockNames[typ]
		if label == "" {
			label = monsterNames[typ]
		}

		if label == "" {
			label = fmt.Sprintf("Type %d", typ)
		}

		c.Objects = append(c.Objects, Object{
			X:          int(int32(u32(p))) / 160,
			Z:          int(int32(u32(p+4))) / 160,
			Y:          s16(p+8)/160 + addY,
			Width:      s8(p + 0x0A),
			Height:     s8(p + 0x0B),
			Flags:      flags,
			ChildFlags: u32(p + 0x10),
			Extended:   u32(p + 0x14),
			Type:       typ,
			ChildType:  s8(p + 0x19),
			LinkID:     s16(p + 0x1A),
			Effect:     s16(p + 0x1C),
			Transform:  s8(p + 0x1E),
			ChildXform: s8(p + 0x1F),
			Size:       size,
			SubType:    sub,
			Wing:       int((flags & 7 & 2) >> 1),
			Block:      isBlock,
			Name:       label,
		})
	}

	return c, nil
}

func trimNul(b []byte) []byte {
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}

	return b
}
