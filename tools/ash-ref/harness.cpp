// Test harness: runs the reference decoder (SMMDB/ASH0/Ash.cpp) on a file.
//   harness <in.ash0> <out.bin>
#include "Ash.h"
#include <cstdio>
#include <fstream>
#include <iterator>
#include <vector>

int main(int argc, char** argv) {
    if (argc != 3) { std::fprintf(stderr, "usage: harness in out\n"); return 2; }
    std::ifstream in(argv[1], std::ios::binary);
    std::vector<uint8_t> data((std::istreambuf_iterator<char>(in)), std::istreambuf_iterator<char>());
    std::vector<uint8_t> out = Ash::decompress(data);
    std::ofstream o(argv[2], std::ios::binary);
    o.write(reinterpret_cast<const char*>(out.data()), out.size());
    std::printf("%zu\n", out.size());
    return out.empty() ? 1 : 0;
}
