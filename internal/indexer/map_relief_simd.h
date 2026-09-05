// Exact SIMD relief: no FMA, reciprocal estimates or reassociation. CPU/OS
// checks in the Go dispatcher guard each target-specific function.
#include <immintrin.h>
#include <string.h>

#define GH_TARGET __attribute__((target("avx2,no-fma")))
#define GH_VEC __m256d
#define GH_LANES 4
#define GH_V(name) _mm256_##name
#define GH_LOAD gh_relief_load_avx2
#define GH_STORE gh_relief_store_avx2
#define GH_ROW gh_relief_row_avx2
#include "map_relief_simd_impl.h"
#undef GH_TARGET
#undef GH_VEC
#undef GH_LANES
#undef GH_V
#undef GH_LOAD
#undef GH_STORE
#undef GH_ROW

#define GH_TARGET __attribute__((target("avx2,avx512f,avx512bw,avx512dq,no-fma")))
#define GH_VEC __m512d
#define GH_LANES 8
#define GH_V(name) _mm512_##name
#define GH_LOAD gh_relief_load_avx512
#define GH_STORE gh_relief_store_avx512
#define GH_ROW gh_relief_row_avx512
#include "map_relief_simd_impl.h"
#undef GH_TARGET
#undef GH_VEC
#undef GH_LANES
#undef GH_V
#undef GH_LOAD
#undef GH_STORE
#undef GH_ROW
