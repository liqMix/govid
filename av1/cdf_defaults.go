package av1

// Default CDF tables from AV1 spec Section 9.3.
// Each CDF array ends with a 0 counter (adaptation count).
// Values are pre-scaled to the CDF range [0, 32768).
// Non-coefficient CDFs from libaom entropymode.c.
// Coefficient CDFs from dav1d cdf.c Q1 set (base_q_idx 21-84).

// initDefaultCDFs populates a CDFTables with spec-default values.
// baseQIdx selects Q-specific coefficient CDFs. Pass -1 for universal defaults.
func initDefaultCDFs(c *CDFTables, baseQIdx int) {
	initPartitionCDFs(c)
	initIntraModeCDFs(c)
	initTXSizeCDFs(c)
	initTXTypeCDFs(c)
	initSkipCDFs(c)
	initCoeffCDFs(c, baseQIdx)
	initDeltaQCDFs(c)
	initCfLCDFs(c)
	initFilterIntraCDFs(c)
	initSegmentIDCDFs(c)
	initPaletteCDFs(c)
}

// makeCDF creates a CDF slice for nsymbs = len(vals)+1 symbols.
// Input vals are ascending cumulative frequencies from libaom/spec.
// Storage is descending (32768 - v), matching dav1d's CDF1 macro.
// Layout: [descending vals..., 0 (implicit last boundary), 0 (adaptation counter)]
// Total length = len(vals) + 2 = nsymbs + 1.
func makeCDF(vals ...uint16) []uint16 {
	cdf := make([]uint16, len(vals)+2)
	for i, v := range vals {
		cdf[i] = 32768 - v
	}
	return cdf
}

// ---- Partition CDFs (libaom default_partition_cdf) ----
// Our context = bsl*4 + 2*left + above where bsl = BlockWidthLog2[bSize].
// bsl=1 (8x8) → ctx 4-7: 4 symbols. bsl≥2 → ctx 8-23: 10 symbols.
// libaom uses bsl-1 offset so libaom ctx N = our ctx N+4.

func initPartitionCDFs(c *CDFTables) {
	// ctx = bsl*4 + 2*left + above, bsl = BlockWidthLog2 - 1.
	// bsl=0 (8x8): ctx 0-3, 4 symbols.
	c.Partition[0] = makeCDF(19132, 25510, 30392)
	c.Partition[1] = makeCDF(13928, 19855, 28540)
	c.Partition[2] = makeCDF(12522, 23679, 28629)
	c.Partition[3] = makeCDF(9896, 18783, 25853)
	// bsl=1 (16x16): ctx 4-7, 10 symbols.
	c.Partition[4] = makeCDF(15597, 20929, 24571, 26706, 27664, 28821, 29601, 30571, 31902)
	c.Partition[5] = makeCDF(7925, 11043, 16785, 22470, 23971, 25043, 26651, 28701, 29834)
	c.Partition[6] = makeCDF(5414, 13269, 15111, 20488, 22360, 24500, 25537, 26336, 32117)
	c.Partition[7] = makeCDF(2662, 6362, 8614, 20860, 23053, 24778, 26436, 27829, 31171)
	// bsl=2 (32x32): ctx 8-11.
	c.Partition[8] = makeCDF(18462, 20920, 23124, 27647, 28227, 29049, 29519, 30178, 31544)
	c.Partition[9] = makeCDF(7689, 9060, 12056, 24992, 25660, 26182, 26951, 28041, 29052)
	c.Partition[10] = makeCDF(6015, 9009, 10062, 24544, 25409, 26545, 27071, 27526, 32047)
	c.Partition[11] = makeCDF(1394, 2208, 2796, 28614, 29061, 29466, 29840, 30185, 31899)
	// bsl=3 (64x64): ctx 12-15.
	c.Partition[12] = makeCDF(20137, 21547, 23078, 29566, 29837, 30261, 30524, 30892, 31724)
	c.Partition[13] = makeCDF(6732, 7490, 9497, 27944, 28250, 28515, 28969, 29630, 30104)
	c.Partition[14] = makeCDF(5945, 7663, 8348, 28683, 29117, 29749, 30064, 30298, 32238)
	c.Partition[15] = makeCDF(870, 1212, 1487, 31198, 31394, 31574, 31743, 31881, 32332)
	// bsl=4 (128x128): ctx 16-19, padded to 10 symbols.
	c.Partition[16] = makeCDF(27899, 28219, 28529, 32484, 32539, 32619, 32639, 32700, 32730)
	c.Partition[17] = makeCDF(6607, 6990, 8268, 32060, 32219, 32338, 32371, 32500, 32600)
	c.Partition[18] = makeCDF(5429, 6676, 7122, 32027, 32227, 32531, 32582, 32650, 32700)
	c.Partition[19] = makeCDF(711, 966, 1172, 32448, 32538, 32617, 32664, 32710, 32740)
}

// ---- Intra Mode CDFs ----

func initIntraModeCDFs(c *CDFTables) {
	// Keyframe Y mode CDFs from libaom default_kf_y_mode_cdf[5][5].
	c.KFIntraMode[0][0] = makeCDF(15588, 17027, 19338, 20218, 20682, 21110, 21825, 23244, 24189, 28165, 29093, 30466)
	c.KFIntraMode[0][1] = makeCDF(12016, 18066, 19516, 20303, 20719, 21444, 21888, 23032, 24434, 28658, 30172, 31409)
	c.KFIntraMode[0][2] = makeCDF(10052, 10771, 22296, 22788, 23055, 23239, 24133, 25620, 26160, 29336, 29929, 31567)
	c.KFIntraMode[0][3] = makeCDF(14091, 15406, 16442, 18808, 19136, 19546, 19998, 22096, 24746, 29585, 30958, 32462)
	c.KFIntraMode[0][4] = makeCDF(12122, 13265, 15603, 16501, 18609, 20033, 22391, 25583, 26437, 30261, 31073, 32475)
	c.KFIntraMode[1][0] = makeCDF(10023, 19585, 20848, 21440, 21832, 22760, 23089, 24023, 25381, 29014, 30482, 31436)
	c.KFIntraMode[1][1] = makeCDF(5983, 24099, 24560, 24886, 25066, 25795, 25913, 26423, 27610, 29905, 31276, 31794)
	c.KFIntraMode[1][2] = makeCDF(7444, 12781, 20177, 20728, 21077, 21607, 22170, 23405, 24469, 27915, 29090, 30492)
	c.KFIntraMode[1][3] = makeCDF(8537, 14689, 15432, 17087, 17408, 18172, 18408, 19825, 24649, 29153, 31096, 32210)
	c.KFIntraMode[1][4] = makeCDF(7543, 14231, 15496, 16195, 17905, 20717, 21984, 24516, 26001, 29675, 30981, 31994)
	c.KFIntraMode[2][0] = makeCDF(12613, 13591, 21383, 22004, 22312, 22577, 23401, 25055, 25729, 29538, 30305, 32077)
	c.KFIntraMode[2][1] = makeCDF(9687, 13470, 18506, 19230, 19604, 20147, 20695, 22062, 23219, 27743, 29211, 30907)
	c.KFIntraMode[2][2] = makeCDF(6183, 6505, 26024, 26252, 26366, 26434, 27082, 28354, 28555, 30467, 30794, 32086)
	c.KFIntraMode[2][3] = makeCDF(10718, 11734, 14954, 17224, 17565, 17924, 18561, 21523, 23878, 28975, 30287, 32252)
	c.KFIntraMode[2][4] = makeCDF(9194, 9858, 16501, 17263, 18424, 19171, 21563, 25961, 26561, 30072, 30737, 32463)
	c.KFIntraMode[3][0] = makeCDF(12602, 14399, 15488, 18381, 18778, 19315, 19724, 21419, 25060, 29696, 30917, 32409)
	c.KFIntraMode[3][1] = makeCDF(8203, 13821, 14524, 17105, 17439, 18131, 18404, 19468, 25225, 29485, 31158, 32342)
	c.KFIntraMode[3][2] = makeCDF(8451, 9731, 15004, 17643, 18012, 18425, 19070, 21538, 24605, 29118, 30078, 32018)
	c.KFIntraMode[3][3] = makeCDF(7714, 9048, 9516, 16667, 16817, 16994, 17153, 18767, 26743, 30389, 31536, 32528)
	c.KFIntraMode[3][4] = makeCDF(8843, 10280, 11496, 15317, 16652, 17943, 19108, 22718, 25769, 29953, 30983, 32485)
	c.KFIntraMode[4][0] = makeCDF(12578, 13671, 15979, 16834, 19075, 20913, 22989, 25449, 26219, 30214, 31150, 32477)
	c.KFIntraMode[4][1] = makeCDF(9563, 13626, 15080, 15892, 17756, 20863, 22207, 24236, 25380, 29653, 31143, 32277)
	c.KFIntraMode[4][2] = makeCDF(8356, 8901, 17616, 18256, 19350, 20106, 22598, 25947, 26466, 29900, 30523, 32261)
	c.KFIntraMode[4][3] = makeCDF(10835, 11815, 13124, 16042, 17018, 18039, 18947, 22753, 24615, 29489, 30883, 32482)
	c.KFIntraMode[4][4] = makeCDF(7618, 8288, 9859, 10509, 15386, 18657, 22903, 28776, 29180, 31355, 31802, 32593)

	// Inter-frame Y mode CDFs (libaom default_if_y_mode_cdf).
	c.IntraMode[0] = makeCDF(22801, 23489, 24293, 24756, 25601, 26123, 26606, 27418, 27945, 29228, 29685, 30349)
	c.IntraMode[1] = makeCDF(18673, 19845, 22631, 23318, 23950, 24649, 25527, 27364, 28152, 29701, 29984, 30852)
	c.IntraMode[2] = makeCDF(19770, 20979, 23396, 23939, 24241, 24654, 25136, 27073, 27830, 29360, 29730, 30659)
	c.IntraMode[3] = makeCDF(20155, 21301, 22838, 23178, 23261, 23533, 23703, 24804, 25352, 26575, 27016, 28049)

	// UV mode CDFs per Y mode, without CfL (libaom default_uv_mode_cdf[0]).
	c.IntraModeUV[0] = makeCDF(22631, 24152, 25378, 25661, 25986, 26520, 27055, 27923, 28244, 30059, 30941, 31961)
	c.IntraModeUV[1] = makeCDF(9513, 26881, 26973, 27046, 27118, 27664, 27739, 27824, 28359, 29505, 29800, 31796)
	c.IntraModeUV[2] = makeCDF(9845, 9915, 28663, 28704, 28757, 28780, 29198, 29822, 29854, 30764, 31777, 32029)
	c.IntraModeUV[3] = makeCDF(13639, 13897, 14171, 25331, 25606, 25727, 25953, 27148, 28577, 30612, 31355, 32493)
	c.IntraModeUV[4] = makeCDF(9764, 9835, 9930, 9954, 25386, 27053, 27958, 28148, 28243, 31101, 31744, 32363)
	c.IntraModeUV[5] = makeCDF(11825, 13589, 13677, 13720, 15048, 29213, 29301, 29458, 29711, 31161, 31441, 32550)
	c.IntraModeUV[6] = makeCDF(14175, 14399, 16608, 16821, 17718, 17775, 28551, 30200, 30245, 31837, 32342, 32667)
	c.IntraModeUV[7] = makeCDF(12885, 13038, 14978, 15590, 15673, 15748, 16176, 29128, 29267, 30643, 31961, 32461)
	c.IntraModeUV[8] = makeCDF(12026, 13661, 13874, 15305, 15490, 15726, 15995, 16273, 28443, 30388, 30767, 32416)
	c.IntraModeUV[9] = makeCDF(19052, 19840, 20579, 20916, 21150, 21467, 21885, 22719, 23174, 28861, 30379, 32175)
	c.IntraModeUV[10] = makeCDF(18627, 19649, 20974, 21219, 21492, 21816, 22199, 23119, 23527, 27053, 31397, 32148)
	c.IntraModeUV[11] = makeCDF(17026, 19004, 19997, 20339, 20586, 21103, 21349, 21907, 22482, 25896, 26541, 31819)
	c.IntraModeUV[12] = makeCDF(12124, 13759, 14959, 14992, 15007, 15051, 15078, 15166, 15255, 15753, 16039, 16606)
	c.IntraModeUV[13] = makeCDF(22631, 24152, 25378, 25661, 25986, 26520, 27055, 27923, 28244, 30059, 30941, 31961)

	// Angle delta CDFs per direction (libaom default_angle_delta_cdf).
	c.AngleDelta[0] = makeCDF(2180, 5032, 7567, 22776, 26989, 30217)
	c.AngleDelta[1] = makeCDF(2301, 5608, 8801, 23487, 26974, 30330)
	c.AngleDelta[2] = makeCDF(3780, 11018, 13699, 19354, 23083, 31286)
	c.AngleDelta[3] = makeCDF(4581, 11226, 15147, 17138, 21834, 28397)
	c.AngleDelta[4] = makeCDF(1737, 10927, 14509, 19588, 22745, 28823)
	c.AngleDelta[5] = makeCDF(2664, 10176, 12485, 17650, 21600, 30495)
	c.AngleDelta[6] = makeCDF(2240, 11096, 15453, 20341, 22561, 28917)
	c.AngleDelta[7] = makeCDF(3605, 10428, 12459, 17676, 21244, 30655)
}

// ---- TX Size CDFs (spec 9.3.3) ----

func initTXSizeCDFs(c *CDFTables) {
	c.TXSize[0][0] = makeCDF(16384)
	c.TXSize[0][1] = makeCDF(16384)
	c.TXSize[0][2] = makeCDF(16384)
	c.TXSize[1][0] = makeCDF(19968)
	c.TXSize[1][1] = makeCDF(16384)
	c.TXSize[1][2] = makeCDF(16384)
	c.TXSize[2][0] = makeCDF(12986)
	c.TXSize[2][1] = makeCDF(15045)
	c.TXSize[2][2] = makeCDF(16384)
	c.TXSize[3][0] = makeCDF(2040)
	c.TXSize[3][1] = makeCDF(8973)
	c.TXSize[3][2] = makeCDF(16384)
	c.TXSize[4][0] = makeCDF(2040)
	c.TXSize[4][1] = makeCDF(8973)
	c.TXSize[4][2] = makeCDF(16384)
	c.TXSize[5][0] = makeCDF(16384)
	c.TXSize[5][1] = makeCDF(16384)
	c.TXSize[5][2] = makeCDF(16384)
}

// ---- TX Type CDFs ----

func initTXTypeCDFs(c *CDFTables) {
	for tsq := 0; tsq < 4; tsq++ {
		for dir := 0; dir < 16; dir++ {
			vals := make([]uint16, 15)
			for i := 0; i < 15; i++ {
				vals[i] = uint16((int(i+1) * 32768) / 16)
			}
			c.TXType[tsq][dir] = makeCDF(vals...)
		}
	}
}

// ---- Skip CDFs ----

func initSkipCDFs(c *CDFTables) {
	c.Skip[0] = makeCDF(31671)
	c.Skip[1] = makeCDF(28510)
	c.Skip[2] = makeCDF(22838)
}

// ---- Coefficient CDFs (dav1d default_coef_cdf Q1 set) ----
// txSzSqrMap maps our TX size indices to the 5 square TX size contexts.
var txSzSqrMap = [13]int{0, 1, 2, 3, 4, 0, 0, 1, 1, 2, 2, 3, 3}

func initCoeffCDFs(c *CDFTables, baseQIdx int) {
	initTXBSkipCDFs(c)
	initEOBCDFs(c)
	initCoeffBaseEOBCDFs(c)
	initCoeffBaseCDFs(c)
	initCoeffBaseRangeCDFs(c)
	initDCSignCDFs(c)
}

func initCoeffCDFsUniform(c *CDFTables) {
	for txs := 0; txs < 5; txs++ {
		for ctx := 0; ctx < 13; ctx++ {
			c.TXBSkip[txs][ctx] = makeCDF(16384)
		}
	}
	for pt := 0; pt < 2; pt++ {
		c.EOBMulti16[pt] = makeCDF(8192, 16384, 24576, 28672)
		c.EOBMulti32[pt] = makeCDF(6554, 13107, 19661, 24576, 28672)
		c.EOBMulti64[pt] = makeCDF(5461, 10923, 16384, 21845, 26214, 29127)
		c.EOBMulti128[pt] = makeCDF(4681, 9362, 14043, 18725, 23406, 27307, 29491)
		c.EOBMulti256[pt] = makeCDF(4096, 8192, 12288, 16384, 20480, 24576, 27853, 29901)
		c.EOBMulti512[pt] = makeCDF(3641, 7282, 10923, 14564, 18204, 21845, 25486, 28087, 30147)
		c.EOBMulti1024[pt] = makeCDF(3277, 6554, 9830, 13107, 16384, 19661, 22938, 26214, 28491, 30310)
	}
	for ctx := 0; ctx < 9; ctx++ {
		for pt := 0; pt < 2; pt++ {
			c.EOBExtra[ctx][pt] = makeCDF(16384)
		}
	}
	for txs := 0; txs < 3; txs++ {
		for pt := 0; pt < 2; pt++ {
			for ec := 0; ec < 3; ec++ {
				c.CoeffBaseEOB[txs][pt][ec] = makeCDF(17837, 29055)
			}
		}
	}
	for txs := 0; txs < 3; txs++ {
		for pt := 0; pt < 2; pt++ {
			for ctx := 0; ctx < 41; ctx++ {
				c.CoeffBase[txs][pt][ctx] = makeCDF(12160, 23040, 28928)
			}
		}
	}
	for txs := 0; txs < 3; txs++ {
		for pt := 0; pt < 2; pt++ {
			for ctx := 0; ctx < 21; ctx++ {
				c.CoeffBaseRange[txs][pt][ctx] = makeCDF(8192, 16384, 24576)
			}
		}
	}
	for pt := 0; pt < 2; pt++ {
		for ctx := 0; ctx < 3; ctx++ {
			c.DCSign[pt][ctx] = makeCDF(16384)
		}
	}
}

func initTXBSkipCDFs(c *CDFTables) {
	// dav1d Q1: [5 txSzCtx][13 skipCtx]. Full 13-context values.
	txbSkipQ1 := [5][13]uint16{
		{30371, 7570, 13155, 20751, 20969, 27067, 32013, 5495, 17942, 28280, 16384, 16384, 16384},
		{31782, 1836, 10689, 17604, 21622, 27518, 32399, 4419, 16294, 28345, 16384, 16384, 16384},
		{31901, 10311, 18047, 24806, 23288, 27914, 32296, 4215, 15756, 28341, 16384, 16384, 16384},
		{26726, 1045, 11703, 20590, 18554, 25970, 31938, 5583, 21313, 29390, 641, 22265, 31452},
		{26584, 188, 8847, 24519, 22938, 30583, 32608, 16384, 16384, 16384, 16384, 16384, 16384},
	}
	for txs := 0; txs < 5; txs++ {
		for ctx := 0; ctx < 13; ctx++ {
			c.TXBSkip[txs][ctx] = makeCDF(txbSkipQ1[txs][ctx])
		}
	}
}

func initEOBCDFs(c *CDFTables) {
	// EOB multi CDFs from dav1d Q1, using tx_class=0 (2D) values.
	// [0]=luma, [1]=chroma.
	c.EOBMulti16[0] = makeCDF(2125, 2551, 5165, 8946)
	c.EOBMulti16[1] = makeCDF(513, 765, 1859, 6339)

	c.EOBMulti32[0] = makeCDF(989, 1249, 2019, 4151, 10785)
	c.EOBMulti32[1] = makeCDF(313, 441, 1099, 2917, 8562)

	c.EOBMulti64[0] = makeCDF(1260, 1446, 2253, 3712, 6652, 13369)
	c.EOBMulti64[1] = makeCDF(401, 605, 1029, 2563, 5845, 12626)

	c.EOBMulti128[0] = makeCDF(685, 933, 1488, 2714, 4766, 8562, 19254)
	c.EOBMulti128[1] = makeCDF(217, 352, 618, 2303, 5261, 9969, 17472)

	c.EOBMulti256[0] = makeCDF(1448, 2109, 4151, 6263, 9329, 13260, 17944, 23300)
	c.EOBMulti256[1] = makeCDF(399, 1019, 1749, 3038, 10444, 15546, 22739, 27294)

	c.EOBMulti512[0] = makeCDF(1230, 2278, 5035, 7776, 11871, 15346, 19590, 24584, 28749)
	c.EOBMulti512[1] = makeCDF(7265, 9979, 15819, 19250, 21780, 23846, 26478, 28396, 31811)

	c.EOBMulti1024[0] = makeCDF(696, 948, 3145, 5702, 9706, 13217, 17851, 21856, 25692, 28034)
	c.EOBMulti1024[1] = makeCDF(2672, 3591, 9330, 17084, 22725, 24284, 26527, 28027, 28377, 30876)

	// EOB extra bits (used with ReadLiteral, not CDF-coded in our decoder).
	for ctx := 0; ctx < 9; ctx++ {
		for pt := 0; pt < 2; pt++ {
			c.EOBExtra[ctx][pt] = makeCDF(16384)
		}
	}
}

func initCoeffBaseEOBCDFs(c *CDFTables) {
	// dav1d Q1 eob_base_tok: [5 txSzCtx][2 planeType][4 eobCtx], 3 symbols.
	// Our [3 txSizeCtx][2 planeType][3 eobCtx]. Map txSzCtx 0→0, 1→1, 3→2.
	// txSizeCtx=0 (4x4), planeType=0
	c.CoeffBaseEOB[0][0][0] = makeCDF(17560, 29888)
	c.CoeffBaseEOB[0][0][1] = makeCDF(29671, 31549)
	c.CoeffBaseEOB[0][0][2] = makeCDF(31007, 32056)
	// txSizeCtx=0 (4x4), planeType=1
	c.CoeffBaseEOB[0][1][0] = makeCDF(26594, 31212)
	c.CoeffBaseEOB[0][1][1] = makeCDF(31208, 32582)
	c.CoeffBaseEOB[0][1][2] = makeCDF(31835, 32637)
	// txSizeCtx=1 (8x8/16x16), planeType=0
	c.CoeffBaseEOB[1][0][0] = makeCDF(15239, 29932)
	c.CoeffBaseEOB[1][0][1] = makeCDF(31315, 32095)
	c.CoeffBaseEOB[1][0][2] = makeCDF(32130, 32434)
	// txSizeCtx=1, planeType=1
	c.CoeffBaseEOB[1][1][0] = makeCDF(26279, 30968)
	c.CoeffBaseEOB[1][1][1] = makeCDF(31142, 32495)
	c.CoeffBaseEOB[1][1][2] = makeCDF(31713, 32540)
	// txSizeCtx=2 (32x32+), planeType=0 — dav1d txSzCtx=3
	c.CoeffBaseEOB[2][0][0] = makeCDF(1044, 2257)
	c.CoeffBaseEOB[2][0][1] = makeCDF(30755, 31923)
	c.CoeffBaseEOB[2][0][2] = makeCDF(32208, 32693)
	// txSizeCtx=2, planeType=1
	c.CoeffBaseEOB[2][1][0] = makeCDF(21317, 26207)
	c.CoeffBaseEOB[2][1][1] = makeCDF(29133, 30868)
	c.CoeffBaseEOB[2][1][2] = makeCDF(29311, 31231)
}

func initCoeffBaseCDFs(c *CDFTables) {
	// dav1d Q1 base_tok: [5 txSzCtx][2 planeType][41 ctx], 4 symbols.
	// Our [3 txSizeCtx][2 planeType][41 ctx].
	// txSizeCtx=0 (4x4) — dav1d txSzCtx=0, pt=0
	coeffBase0 := [41][3]uint16{
		{6041, 11854, 15927}, {20326, 30905, 32251}, {14164, 26831, 30725},
		{9760, 20647, 26585}, {6416, 14953, 21219}, {2966, 7151, 10891},
		{23567, 31374, 32254}, {14978, 27416, 30946}, {9434, 20225, 26254},
		{6658, 14558, 20535}, {3916, 8677, 12989}, {8192, 16384, 24576},
		{8192, 16384, 24576}, {8192, 16384, 24576}, {8192, 16384, 24576},
		{8192, 16384, 24576}, {8192, 16384, 24576}, {8192, 16384, 24576},
		{8192, 16384, 24576}, {8192, 16384, 24576}, {8192, 16384, 24576},
		{18088, 29545, 31587}, {13062, 25843, 30073}, {8940, 16827, 22251},
		{7654, 13220, 17973}, {5733, 10316, 14456}, {22879, 31388, 32114},
		{15215, 27993, 30955}, {9397, 19445, 24978}, {3442, 9813, 15344},
		{1368, 3936, 6532}, {25494, 32033, 32406}, {16772, 27963, 30718},
		{9419, 18165, 23260}, {2677, 7501, 11797}, {1516, 4344, 7170},
		{26556, 31454, 32101}, {17128, 27035, 30108}, {8324, 15344, 20249},
		{1903, 5696, 9469}, {8192, 16384, 24576},
	}
	for ctx := 0; ctx < 41; ctx++ {
		c.CoeffBase[0][0][ctx] = makeCDF(coeffBase0[ctx][0], coeffBase0[ctx][1], coeffBase0[ctx][2])
		c.CoeffBase[0][1][ctx] = makeCDF(12160, 23040, 28928) // chroma: uniform fallback
	}

	// txSizeCtx=1 (8x8/16x16) — dav1d txSzCtx=1, pt=0
	coeffBase1 := [41][3]uint16{
		{6779, 13743, 17678}, {24806, 31797, 32457}, {17616, 29047, 31372},
		{11063, 23175, 28003}, {6521, 16110, 22324}, {2764, 7504, 11654},
		{25266, 32367, 32637}, {19054, 30553, 32175}, {12139, 25212, 29807},
		{7311, 18162, 24704}, {3397, 9164, 14074}, {25988, 32208, 32522},
		{16253, 28912, 31526}, {9151, 21387, 27372}, {5688, 14915, 21496},
		{2717, 7627, 12004}, {23144, 31855, 32443}, {16070, 28491, 31325},
		{8702, 20467, 26517}, {5243, 13956, 20367}, {2621, 7335, 11567},
		{26636, 32340, 32630}, {19990, 31050, 32341}, {13243, 26105, 30315},
		{8588, 19521, 25918}, {4717, 11585, 17304}, {25844, 32292, 32582},
		{19090, 30635, 32097}, {11963, 24546, 28939}, {6218, 16087, 22354},
		{2340, 6608, 10426}, {28046, 32576, 32694}, {21178, 31313, 32296},
		{13486, 26184, 29870}, {7149, 17871, 23723}, {2833, 7958, 12259},
		{27710, 32528, 32686}, {20674, 31076, 32268}, {12413, 24955, 29243},
		{6676, 16927, 23097}, {2966, 8333, 12919},
	}
	for ctx := 0; ctx < 41; ctx++ {
		c.CoeffBase[1][0][ctx] = makeCDF(coeffBase1[ctx][0], coeffBase1[ctx][1], coeffBase1[ctx][2])
		c.CoeffBase[1][1][ctx] = makeCDF(12160, 23040, 28928)
	}

	// txSizeCtx=2 (32x32+) — dav1d txSzCtx=3, pt=0
	coeffBase2 := [41][3]uint16{
		{3078, 6839, 9890}, {13837, 20450, 24479}, {5914, 14222, 19328},
		{3866, 10267, 14762}, {2612, 7208, 11042}, {1067, 2991, 4776},
		{25817, 31646, 32529}, {13708, 26338, 30385}, {7328, 18585, 24870},
		{4691, 13080, 19276}, {1825, 5253, 8352}, {29386, 32315, 32624},
		{17160, 29001, 31360}, {9602, 21862, 27396}, {5915, 15772, 22148},
		{2786, 7779, 12047}, {29246, 32450, 32663}, {18696, 29929, 31818},
		{10510, 23369, 28560}, {6229, 16499, 23125}, {2608, 7448, 11705},
		{30753, 32710, 32748}, {21638, 31487, 32503}, {12937, 26854, 30870},
		{8182, 20596, 26970}, {3637, 10269, 15497}, {8192, 16384, 24576},
		{8192, 16384, 24576}, {8192, 16384, 24576}, {8192, 16384, 24576},
		{8192, 16384, 24576}, {8192, 16384, 24576}, {8192, 16384, 24576},
		{8192, 16384, 24576}, {8192, 16384, 24576}, {8192, 16384, 24576},
		{8192, 16384, 24576}, {8192, 16384, 24576}, {8192, 16384, 24576},
		{8192, 16384, 24576}, {8192, 16384, 24576},
	}
	for ctx := 0; ctx < 41; ctx++ {
		c.CoeffBase[2][0][ctx] = makeCDF(coeffBase2[ctx][0], coeffBase2[ctx][1], coeffBase2[ctx][2])
		c.CoeffBase[2][1][ctx] = makeCDF(12160, 23040, 28928)
	}
}

func initCoeffBaseRangeCDFs(c *CDFTables) {
	// dav1d Q1 br_tok: [4 txSzCtx][2 planeType][21 ctx], 4 symbols.
	// Our [3 txSizeCtx][21 ctx] uses planeType=0 (luma).
	// txSizeCtx=0 (4x4) — dav1d txSzCtx=0, pt=0, idx 0-20
	br0 := [21][3]uint16{
		{14995, 21341, 24749}, {13158, 20289, 24601}, {8941, 15326, 19876},
		{6297, 11541, 15807}, {4817, 9029, 12776}, {3731, 7273, 10627},
		{1847, 3617, 5354}, {14472, 19659, 22343}, {16806, 24162, 27533},
		{12900, 20404, 24713}, {9411, 16112, 20797}, {7056, 12697, 17148},
		{5544, 10339, 14460}, {2954, 5704, 8319}, {12464, 18071, 21354},
		{15482, 22528, 26034}, {12070, 19269, 23624}, {8953, 15406, 20106},
		{7027, 12730, 17220}, {5887, 10913, 15140}, {3793, 7278, 10447},
	}
	for ctx := 0; ctx < 21; ctx++ {
		c.CoeffBaseRange[0][0][ctx] = makeCDF(br0[ctx][0], br0[ctx][1], br0[ctx][2])
		c.CoeffBaseRange[0][1][ctx] = makeCDF(8192, 16384, 24576)
	}

	// txSizeCtx=1 (8x8/16x16) — dav1d txSzCtx=1, pt=0, idx 42-62
	br1 := [21][3]uint16{
		{15571, 22232, 25749}, {14506, 21575, 25374}, {10189, 17089, 21569},
		{7316, 13301, 17915}, {5783, 10912, 15190}, {4760, 9155, 13088},
		{2993, 5966, 8774}, {23424, 28903, 30778}, {20775, 27666, 30290},
		{16474, 24410, 28299}, {12471, 20180, 24987}, {9410, 16487, 21439},
		{7536, 13614, 18529}, {5048, 9586, 13549}, {21090, 27290, 29756},
		{20796, 27402, 30026}, {17819, 25485, 28969}, {13860, 21909, 26462},
		{11002, 18494, 23529}, {8953, 15929, 20897}, {6448, 11918, 16454},
	}
	for ctx := 0; ctx < 21; ctx++ {
		c.CoeffBaseRange[1][0][ctx] = makeCDF(br1[ctx][0], br1[ctx][1], br1[ctx][2])
		c.CoeffBaseRange[1][1][ctx] = makeCDF(8192, 16384, 24576)
	}

	// txSizeCtx=2 (32x32+) — dav1d txSzCtx=3, pt=0, idx 126-146
	br2 := [21][3]uint16{
		{5705, 10930, 15725}, {7946, 12765, 16115}, {6801, 12123, 16226},
		{5462, 10135, 14200}, {4189, 8011, 11507}, {3191, 6229, 9408},
		{1057, 2137, 3212}, {10018, 17067, 21491}, {7380, 12582, 16453},
		{6068, 10845, 14339}, {5098, 9198, 12555}, {4312, 8010, 11119},
		{3700, 6966, 9781}, {1693, 3326, 4887}, {18757, 24930, 27774},
		{17648, 24596, 27817}, {14707, 22052, 26026}, {11720, 18852, 23292},
		{9357, 15952, 20525}, {7810, 13753, 18210}, {3879, 7333, 10328},
	}
	for ctx := 0; ctx < 21; ctx++ {
		c.CoeffBaseRange[2][0][ctx] = makeCDF(br2[ctx][0], br2[ctx][1], br2[ctx][2])
		c.CoeffBaseRange[2][1][ctx] = makeCDF(8192, 16384, 24576)
	}
}

func initDCSignCDFs(c *CDFTables) {
	// dav1d Q1 dc_sign: [2 planeType][3 ctx].
	c.DCSign[0][0] = makeCDF(16000)
	c.DCSign[0][1] = makeCDF(13056)
	c.DCSign[0][2] = makeCDF(18816)
	c.DCSign[1][0] = makeCDF(15232)
	c.DCSign[1][1] = makeCDF(12928)
	c.DCSign[1][2] = makeCDF(17280)
}

// ---- Delta Q/LF CDFs ----

func initDeltaQCDFs(c *CDFTables) {
	c.DeltaQ = makeCDF(28160, 31999, 32534)
	for i := 0; i < 5; i++ {
		c.DeltaLF[i] = makeCDF(28160, 31999, 32534)
	}
}

func initCfLCDFs(c *CDFTables) {
	// UV mode CDFs with CfL per Y mode (libaom default_uv_mode_cdf[1]).
	c.IntraModeUVCfL[0] = makeCDF(10407, 11208, 12900, 13181, 13823, 14175, 14899, 15656, 15986, 20086, 20995, 22455, 24212)
	c.IntraModeUVCfL[1] = makeCDF(4532, 19780, 20057, 20215, 20428, 21071, 21199, 21451, 22099, 24228, 24693, 27032, 29472)
	c.IntraModeUVCfL[2] = makeCDF(5273, 5379, 20177, 20270, 20385, 20439, 20949, 21695, 21774, 23138, 24256, 24703, 26679)
	c.IntraModeUVCfL[3] = makeCDF(6740, 7167, 7662, 14152, 14536, 14785, 15034, 16741, 18371, 21520, 22206, 23389, 24182)
	c.IntraModeUVCfL[4] = makeCDF(4987, 5368, 5928, 6068, 19114, 20315, 21857, 22253, 22411, 24911, 25380, 26027, 26376)
	c.IntraModeUVCfL[5] = makeCDF(5370, 6889, 7247, 7393, 9498, 21114, 21402, 21753, 21981, 24780, 25386, 26517, 27176)
	c.IntraModeUVCfL[6] = makeCDF(4816, 4961, 7204, 7326, 8765, 8930, 20169, 20682, 20803, 23188, 23763, 24455, 24940)
	c.IntraModeUVCfL[7] = makeCDF(6608, 6740, 8529, 9049, 9257, 9356, 9735, 18827, 19059, 22336, 23204, 23964, 24793)
	c.IntraModeUVCfL[8] = makeCDF(5998, 7419, 7781, 8933, 9255, 9549, 9753, 10417, 18898, 22494, 23139, 24764, 25989)
	c.IntraModeUVCfL[9] = makeCDF(10660, 11298, 12550, 12957, 13322, 13624, 14040, 15004, 15534, 20714, 21789, 23443, 24861)
	c.IntraModeUVCfL[10] = makeCDF(10522, 11530, 12552, 12963, 13378, 13779, 14245, 15235, 15902, 20102, 22696, 23774, 25838)
	c.IntraModeUVCfL[11] = makeCDF(10099, 10691, 12639, 13049, 13386, 13665, 14125, 15163, 15636, 19676, 20474, 23519, 25208)
	c.IntraModeUVCfL[12] = makeCDF(3144, 5087, 7382, 7504, 7593, 7690, 7801, 8064, 8232, 9248, 9875, 10521, 29048)
	c.IntraModeUVCfL[13] = makeCDF(10407, 11208, 12900, 13181, 13823, 14175, 14899, 15656, 15986, 20086, 20995, 22455, 24212)

	c.CfLSign = makeCDF(1418, 2123, 13340, 18405, 26972, 28343, 32294)

	c.CfLAlpha[0] = makeCDF(7637, 20719, 31401, 32481, 32657, 32688, 32692, 32696, 32700, 32704, 32708, 32712, 32716, 32720, 32724)
	c.CfLAlpha[1] = makeCDF(14365, 23603, 28135, 31168, 32167, 32395, 32487, 32573, 32620, 32647, 32668, 32672, 32676, 32680, 32684)
	c.CfLAlpha[2] = makeCDF(11532, 22380, 28445, 31360, 32349, 32523, 32584, 32649, 32673, 32677, 32681, 32685, 32689, 32693, 32697)
	c.CfLAlpha[3] = makeCDF(26990, 31402, 32282, 32571, 32692, 32696, 32700, 32704, 32708, 32712, 32716, 32720, 32724, 32728, 32732)
	c.CfLAlpha[4] = makeCDF(17248, 26058, 28904, 30608, 31305, 31877, 32126, 32321, 32394, 32464, 32516, 32560, 32576, 32593, 32622)
	c.CfLAlpha[5] = makeCDF(14738, 21678, 25779, 27901, 29024, 30302, 30980, 31843, 32144, 32413, 32520, 32594, 32622, 32656, 32660)
}

func initFilterIntraCDFs(c *CDFTables) {
	c.FilterIntraMode = makeCDF(8192, 16384, 24576, 28672)
	for i := 0; i < 22; i++ {
		c.UseFilterIntra[i] = makeCDF(16384)
	}
}

func initSegmentIDCDFs(c *CDFTables) {
	for i := 0; i < 3; i++ {
		c.SegmentID[i] = makeCDF(4096, 8192, 12288, 16384, 20480, 24576, 28672)
	}
}

func initPaletteCDFs(c *CDFTables) {
	// libaom default_palette_y_mode_cdf.
	c.PaletteY[0][0] = makeCDF(31676)
	c.PaletteY[0][1] = makeCDF(3419)
	c.PaletteY[0][2] = makeCDF(1261)
	c.PaletteY[1][0] = makeCDF(31912)
	c.PaletteY[1][1] = makeCDF(2859)
	c.PaletteY[1][2] = makeCDF(980)
	c.PaletteY[2][0] = makeCDF(31823)
	c.PaletteY[2][1] = makeCDF(3400)
	c.PaletteY[2][2] = makeCDF(781)
	c.PaletteY[3][0] = makeCDF(32030)
	c.PaletteY[3][1] = makeCDF(3561)
	c.PaletteY[3][2] = makeCDF(904)
	c.PaletteY[4][0] = makeCDF(32309)
	c.PaletteY[4][1] = makeCDF(7337)
	c.PaletteY[4][2] = makeCDF(1462)
	c.PaletteY[5][0] = makeCDF(32265)
	c.PaletteY[5][1] = makeCDF(4015)
	c.PaletteY[5][2] = makeCDF(1521)
	c.PaletteY[6][0] = makeCDF(32450)
	c.PaletteY[6][1] = makeCDF(7946)
	c.PaletteY[6][2] = makeCDF(129)
	// libaom default_palette_uv_mode_cdf.
	c.PaletteUV[0] = makeCDF(32461)
	c.PaletteUV[1] = makeCDF(21488)

	// Palette size CDFs (libaom default_palette_y_size_cdf / uv).
	c.PaletteSizeY[0] = makeCDF(7952, 13000, 18149, 21478, 25527, 29241)
	c.PaletteSizeY[1] = makeCDF(7139, 11421, 16195, 19544, 23666, 28073)
	c.PaletteSizeY[2] = makeCDF(7788, 12741, 17325, 20500, 24315, 28530)
	c.PaletteSizeY[3] = makeCDF(8271, 14064, 18246, 21564, 25071, 28533)
	c.PaletteSizeY[4] = makeCDF(12725, 19180, 21863, 24839, 27535, 30120)
	c.PaletteSizeY[5] = makeCDF(9711, 14888, 16923, 21052, 25661, 27875)
	c.PaletteSizeY[6] = makeCDF(14940, 20797, 21678, 24186, 27033, 28999)
	c.PaletteSizeUV[0] = makeCDF(8713, 19979, 27128, 29609, 31331, 32272)
	c.PaletteSizeUV[1] = makeCDF(5839, 15573, 23581, 26947, 29848, 31700)
	c.PaletteSizeUV[2] = makeCDF(4426, 11260, 17999, 21483, 25863, 29430)
	c.PaletteSizeUV[3] = makeCDF(3228, 9464, 14993, 18089, 22523, 27420)
	c.PaletteSizeUV[4] = makeCDF(3768, 8886, 13091, 17852, 22495, 27207)
	c.PaletteSizeUV[5] = makeCDF(2464, 8451, 12861, 21632, 25525, 28555)
	c.PaletteSizeUV[6] = makeCDF(1269, 5435, 10433, 18963, 21700, 25865)

	// Palette color index CDFs (libaom default_palette_y_color_index_cdf).
	// [palette_size-2][context], each with palette_size symbols.
	// palette_size=2
	c.PaletteColorIdx[0][0] = makeCDF(28710)
	c.PaletteColorIdx[0][1] = makeCDF(16384)
	c.PaletteColorIdx[0][2] = makeCDF(10553)
	c.PaletteColorIdx[0][3] = makeCDF(27036)
	c.PaletteColorIdx[0][4] = makeCDF(31603)
	// palette_size=3
	c.PaletteColorIdx[1][0] = makeCDF(27877, 30490)
	c.PaletteColorIdx[1][1] = makeCDF(11532, 25697)
	c.PaletteColorIdx[1][2] = makeCDF(6544, 30234)
	c.PaletteColorIdx[1][3] = makeCDF(23018, 28072)
	c.PaletteColorIdx[1][4] = makeCDF(31915, 32385)
	// palette_size=4
	c.PaletteColorIdx[2][0] = makeCDF(25572, 28046, 30045)
	c.PaletteColorIdx[2][1] = makeCDF(9478, 21590, 27256)
	c.PaletteColorIdx[2][2] = makeCDF(7248, 26837, 29824)
	c.PaletteColorIdx[2][3] = makeCDF(19167, 24486, 28349)
	c.PaletteColorIdx[2][4] = makeCDF(31400, 31825, 32250)
	// palette_size=5
	c.PaletteColorIdx[3][0] = makeCDF(24104, 26383, 28300, 30584)
	c.PaletteColorIdx[3][1] = makeCDF(7443, 17242, 24461, 28709)
	c.PaletteColorIdx[3][2] = makeCDF(5765, 23540, 27553, 30885)
	c.PaletteColorIdx[3][3] = makeCDF(17514, 22188, 26509, 30500)
	c.PaletteColorIdx[3][4] = makeCDF(31198, 31532, 31866, 32200)
	// palette_size=6
	c.PaletteColorIdx[4][0] = makeCDF(23105, 25199, 27218, 29064, 31069)
	c.PaletteColorIdx[4][1] = makeCDF(6950, 15447, 22614, 27327, 30519)
	c.PaletteColorIdx[4][2] = makeCDF(4891, 20673, 25270, 28791, 31560)
	c.PaletteColorIdx[4][3] = makeCDF(16384, 20352, 24320, 28288, 31346)
	c.PaletteColorIdx[4][4] = makeCDF(30846, 31156, 31466, 31776, 32086)
	// palette_size=7
	c.PaletteColorIdx[5][0] = makeCDF(22412, 24428, 26229, 28291, 29994, 31558)
	c.PaletteColorIdx[5][1] = makeCDF(6480, 14300, 21036, 26178, 29425, 31490)
	c.PaletteColorIdx[5][2] = makeCDF(4492, 19539, 24234, 27755, 30440, 32008)
	c.PaletteColorIdx[5][3] = makeCDF(14301, 17967, 21633, 25299, 28965, 31477)
	c.PaletteColorIdx[5][4] = makeCDF(30553, 30842, 31131, 31420, 31709, 31998)
	// palette_size=8
	c.PaletteColorIdx[6][0] = makeCDF(21909, 23611, 25313, 27281, 28953, 30465, 31825)
	c.PaletteColorIdx[6][1] = makeCDF(6472, 13584, 19766, 24938, 28259, 30614, 32055)
	c.PaletteColorIdx[6][2] = makeCDF(4233, 18434, 23364, 26890, 29728, 31478, 32277)
	c.PaletteColorIdx[6][3] = makeCDF(12683, 15853, 19023, 22193, 25363, 28533, 31482)
	c.PaletteColorIdx[6][4] = makeCDF(30277, 30546, 30815, 31084, 31353, 31622, 31891)
}
