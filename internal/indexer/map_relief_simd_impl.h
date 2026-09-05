// Intentionally included twice. GH_* selects 4 or 8 independent pixels;
// the arithmetic is shared so fixes cannot drift between ISA variants.
GH_TARGET static inline GH_VEC GH_LOAD(const uint8_t *p, int big_endian)
{
#if GH_LANES == 8
    __m128i packed = _mm_loadu_si128((const __m128i *)p);
#else
    __m128i packed = _mm_loadl_epi64((const __m128i *)p);
#endif
    if (big_endian) packed = _mm_shuffle_epi8(packed,
        _mm_setr_epi8(1,0,3,2,5,4,7,6,9,8,11,10,13,12,15,14));
#if GH_LANES == 8
    __m256i ints = _mm256_cvtepu16_epi32(packed);
#else
    __m128i ints = _mm_cvtepu16_epi32(packed);
#endif
    return GH_V(mul_pd)(GH_V(cvtepi32_pd)(ints), GH_V(set1_pd)(1.0 / 65535.0));
}

GH_TARGET static inline void GH_STORE(uint8_t *p, GH_VEC v)
{
#if GH_LANES == 8
    __m256i ints = _mm512_cvttpd_epi32(_mm512_add_pd(v, _mm512_set1_pd(0.5)));
    __m128i words = _mm_packus_epi32(_mm256_castsi256_si128(ints), _mm256_extracti128_si256(ints, 1));
    uint64_t packed = (uint64_t)_mm_cvtsi128_si64(_mm_packus_epi16(words, words));
#else
    __m128i ints = _mm256_cvttpd_epi32(_mm256_add_pd(v, _mm256_set1_pd(0.5)));
    __m128i words = _mm_packus_epi32(ints, ints);
    uint32_t packed = (uint32_t)_mm_cvtsi128_si32(_mm_packus_epi16(words, words));
#endif
    memcpy(p, &packed, sizeof(packed));
}

// Process only complete vectors whose +/-5 taps stay inside the row. Scalar
// code handles both borders, narrow images, and the incomplete final vector.
GH_TARGET static inline int GH_ROW(const uint8_t *const rp[11], int width,
	uint8_t *hill, uint8_t *detail, uint8_t *elev,
	double keyX, double keyY, double keyZ, double fillX, double fillY, double fillZ,
	int big_endian)
{
	const GH_VEC zero = GH_V(setzero_pd)();
	const GH_VEC one = GH_V(set1_pd)(1.0);
	const GH_VEC scale255 = GH_V(set1_pd)(255.0);
	int x = 5;
	for (; x <= width - 5 - GH_LANES; x += GH_LANES) {
		GH_VEC h0 = GH_LOAD(rp[5] + 2*(x), big_endian);
		GH_VEC dxFine = GH_V(mul_pd)(GH_V(sub_pd)(GH_LOAD(rp[5] + 2*(x+1), big_endian), GH_LOAD(rp[5] + 2*(x-1), big_endian)), GH_V(set1_pd)(9.0));
		GH_VEC dyFine = GH_V(mul_pd)(GH_V(sub_pd)(GH_LOAD(rp[6] + 2*(x), big_endian), GH_LOAD(rp[4] + 2*(x), big_endian)), GH_V(set1_pd)(9.0));
		GH_VEC dxBroad = GH_V(mul_pd)(GH_V(sub_pd)(GH_LOAD(rp[5] + 2*(x+4), big_endian), GH_LOAD(rp[5] + 2*(x-4), big_endian)), GH_V(set1_pd)(2.25));
		GH_VEC dyBroad = GH_V(mul_pd)(GH_V(sub_pd)(GH_LOAD(rp[9] + 2*(x), big_endian), GH_LOAD(rp[1] + 2*(x), big_endian)), GH_V(set1_pd)(2.25));
		GH_VEC nx = GH_V(xor_pd)(GH_V(add_pd)(GH_V(mul_pd)(dxFine, GH_V(set1_pd)(0.62)), GH_V(mul_pd)(dxBroad, GH_V(set1_pd)(0.38))), GH_V(set1_pd)(-0.0));
		GH_VEC ny = GH_V(xor_pd)(GH_V(add_pd)(GH_V(mul_pd)(dyFine, GH_V(set1_pd)(0.62)), GH_V(mul_pd)(dyBroad, GH_V(set1_pd)(0.38))), GH_V(set1_pd)(-0.0));
		GH_VEC nz;
		GH_VEC length = GH_V(sqrt_pd)(GH_V(add_pd)(GH_V(add_pd)(GH_V(mul_pd)(nx, nx), GH_V(mul_pd)(ny, ny)), one));
		nx = GH_V(div_pd)(nx, length);
		ny = GH_V(div_pd)(ny, length);
		nz = GH_V(div_pd)(one, length);
		GH_VEC key = GH_V(max_pd)(zero, GH_V(add_pd)(GH_V(add_pd)(GH_V(mul_pd)(nx, GH_V(set1_pd)(keyX)), GH_V(mul_pd)(ny, GH_V(set1_pd)(keyY))), GH_V(mul_pd)(nz, GH_V(set1_pd)(keyZ))));
		GH_VEC fill = GH_V(max_pd)(zero, GH_V(add_pd)(GH_V(add_pd)(GH_V(mul_pd)(nx, GH_V(set1_pd)(fillX)), GH_V(mul_pd)(ny, GH_V(set1_pd)(fillY))), GH_V(mul_pd)(nz, GH_V(set1_pd)(fillZ))));
		GH_VEC shade = GH_V(add_pd)(GH_V(mul_pd)(key, GH_V(set1_pd)(0.72)), GH_V(mul_pd)(fill, GH_V(set1_pd)(0.28)));
		GH_VEC broadMean = GH_V(add_pd)(GH_LOAD(rp[5] + 2*(x-5), big_endian), GH_LOAD(rp[5] + 2*(x+5), big_endian));
		broadMean = GH_V(add_pd)(broadMean, GH_LOAD(rp[0] + 2*(x), big_endian));
		broadMean = GH_V(mul_pd)(GH_V(add_pd)(broadMean, GH_LOAD(rp[10] + 2*(x), big_endian)), GH_V(set1_pd)(0.25));
		GH_VEC fineMean = GH_V(add_pd)(GH_LOAD(rp[5] + 2*(x-2), big_endian), GH_LOAD(rp[5] + 2*(x+2), big_endian));
		fineMean = GH_V(add_pd)(fineMean, GH_LOAD(rp[3] + 2*(x), big_endian));
		fineMean = GH_V(mul_pd)(GH_V(add_pd)(fineMean, GH_LOAD(rp[7] + 2*(x), big_endian)), GH_V(set1_pd)(0.25));
		GH_VEC curvature = GH_V(add_pd)(GH_V(mul_pd)(GH_V(sub_pd)(h0, fineMean), GH_V(set1_pd)(42.0)), GH_V(mul_pd)(GH_V(sub_pd)(h0, broadMean), GH_V(set1_pd)(18.0)));
		shade = GH_V(add_pd)(shade, GH_V(max_pd)(GH_V(set1_pd)(-0.10), GH_V(min_pd)(GH_V(set1_pd)(0.10), GH_V(mul_pd)(curvature, GH_V(set1_pd)(0.12)))));
		shade = GH_V(max_pd)(zero, GH_V(min_pd)(one, shade));
		GH_VEC hi = GH_V(add_pd)(GH_V(set1_pd)(0.12), GH_V(mul_pd)(shade, GH_V(set1_pd)(0.88)));
		hi = GH_V(mul_pd)(GH_V(max_pd)(zero, GH_V(min_pd)(one, hi)), scale255);
		GH_VEC de = GH_V(mul_pd)(GH_V(max_pd)(zero, GH_V(min_pd)(one, GH_V(add_pd)(GH_V(set1_pd)(0.5), curvature))), scale255);
		GH_STORE(hill+x, hi);
		GH_STORE(detail+x, de);
		GH_STORE(elev+x, GH_V(mul_pd)(h0, scale255));
	}
	return x;
}
