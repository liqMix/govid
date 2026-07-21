package av1

// Default CDF tables from AV1 spec Section 9.3.
// Each CDF array ends with a 0 counter (adaptation count).
// Values are pre-scaled to the CDF range [0).
// Non-coefficient CDFs from libaom entropymode.c.
// Coefficient CDFs from dav1d cdf.c, all 4 Q categories (Q0-Q3).

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

// makeCDF creates a CDF slice. Input vals are ascending cumulative frequencies.
// Storage: [ICDF values..., 0 (adaptation counter)]
// Total length = len(vals) + 1. nsymbs = len(vals) + 1.
func makeCDF(vals ...uint16) []uint16 {
	cdf := make([]uint16, len(vals)+1)
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
	// Partition CDFs indexed by [block_level][ctx].
	// BL0=128x128 (8 symbols), BL1-3 (10 symbols), BL4=8x8 (4 symbols).

	// BL0: 128x128, 8 symbols (no HORZ_4/VERT_4).
	c.Partition[0][0] = makeCDF(27899, 28219, 28529, 32484, 32539, 32619, 32639)
	c.Partition[0][1] = makeCDF(6607, 6990, 8268, 32060, 32219, 32338, 32371)
	c.Partition[0][2] = makeCDF(5429, 6676, 7122, 32027, 32227, 32531, 32582)
	c.Partition[0][3] = makeCDF(711, 966, 1172, 32448, 32538, 32617, 32664)

	// BL1: 64x64, 10 symbols.
	c.Partition[1][0] = makeCDF(20137, 21547, 23078, 29566, 29837, 30261, 30524, 30892, 31724)
	c.Partition[1][1] = makeCDF(6732, 7490, 9497, 27944, 28250, 28515, 28969, 29630, 30104)
	c.Partition[1][2] = makeCDF(5945, 7663, 8348, 28683, 29117, 29749, 30064, 30298, 32238)
	c.Partition[1][3] = makeCDF(870, 1212, 1487, 31198, 31394, 31574, 31743, 31881, 32332)

	// BL2: 32x32, 10 symbols.
	c.Partition[2][0] = makeCDF(18462, 20920, 23124, 27647, 28227, 29049, 29519, 30178, 31544)
	c.Partition[2][1] = makeCDF(7689, 9060, 12056, 24992, 25660, 26182, 26951, 28041, 29052)
	c.Partition[2][2] = makeCDF(6015, 9009, 10062, 24544, 25409, 26545, 27071, 27526, 32047)
	c.Partition[2][3] = makeCDF(1394, 2208, 2796, 28614, 29061, 29466, 29840, 30185, 31899)

	// BL3: 16x16, 10 symbols.
	c.Partition[3][0] = makeCDF(15597, 20929, 24571, 26706, 27664, 28821, 29601, 30571, 31902)
	c.Partition[3][1] = makeCDF(7925, 11043, 16785, 22470, 23971, 25043, 26651, 28701, 29834)
	c.Partition[3][2] = makeCDF(5414, 13269, 15111, 20488, 22360, 24500, 25537, 26336, 32117)
	c.Partition[3][3] = makeCDF(2662, 6362, 8614, 20860, 23053, 24778, 26436, 27829, 31171)

	// BL4: 8x8, 4 symbols (NONE, HORZ, VERT, SPLIT).
	c.Partition[4][0] = makeCDF(19132, 25510, 30392)
	c.Partition[4][1] = makeCDF(13928, 19855, 28540)
	c.Partition[4][2] = makeCDF(12522, 23679, 28629)
	c.Partition[4][3] = makeCDF(9896, 18783, 25853)
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
	// dav1d txpart[7][3]: flat index by (maxTx, depth) pair, 3 neighbor contexts.
	// Index 0: TX8x8 split (maxTx=1, depth=0)
	c.TXSize[0][0] = makeCDF(28581)
	c.TXSize[0][1] = makeCDF(23846)
	c.TXSize[0][2] = makeCDF(20847)
	// Index 1: TX16x16 split (maxTx≥2, depth=0)
	c.TXSize[1][0] = makeCDF(24315)
	c.TXSize[1][1] = makeCDF(18196)
	c.TXSize[1][2] = makeCDF(12133)
	// Index 2: TX8x8 split after TX16x16 (maxTx=2, depth=1)
	c.TXSize[2][0] = makeCDF(18791)
	c.TXSize[2][1] = makeCDF(10887)
	c.TXSize[2][2] = makeCDF(11005)
	// Index 3: TX32x32 split (maxTx≥3, depth=0)
	c.TXSize[3][0] = makeCDF(27179)
	c.TXSize[3][1] = makeCDF(20004)
	c.TXSize[3][2] = makeCDF(11281)
	// Index 4: TX16x16 split after TX32x32 (maxTx≥3, depth=1)
	c.TXSize[4][0] = makeCDF(26549)
	c.TXSize[4][1] = makeCDF(19308)
	c.TXSize[4][2] = makeCDF(14224)
	// Index 5: TX64x64 split (maxTx=4, depth=0)
	c.TXSize[5][0] = makeCDF(28015)
	c.TXSize[5][1] = makeCDF(21546)
	c.TXSize[5][2] = makeCDF(14400)
	// Index 6: TX32x32 split after TX64x64 (maxTx=4, depth=1)
	c.TXSize[6][0] = makeCDF(28165)
	c.TXSize[6][1] = makeCDF(22401)
	c.TXSize[6][2] = makeCDF(16088)
}

// ---- TX Type CDFs ----
// dav1d has two sets:
// txtp_intra1[2][13]: 6-symbol CDF for TX4x4/TX8x8 non-reduced (our 7 nsymbs)
// txtp_intra2[3][13]: 4-symbol CDF for reduced OR TX16x16 (our 5 nsymbs)

func initTXTypeCDFs(c *CDFTables) {
	// IntraTXType1: dav1d txtp_intra1 — non-reduced, TX4x4 (min=0) and TX8x8 (min=1)
	// CDF6: 6 symbols (our nsymbs=7)
	c.IntraTXType1[0][IntraDC] = makeCDF(1535, 8035, 9461, 12751, 23467, 27825)
	c.IntraTXType1[0][IntraVertical] = makeCDF(564, 3335, 9709, 10870, 18143, 28094)
	c.IntraTXType1[0][IntraHorizontal] = makeCDF(672, 3247, 3676, 11982, 19415, 23127)
	c.IntraTXType1[0][IntraD45] = makeCDF(5279, 13885, 15487, 18044, 23527, 30252)
	c.IntraTXType1[0][IntraD135] = makeCDF(4423, 6074, 7985, 10416, 25693, 29298)
	c.IntraTXType1[0][IntraD113] = makeCDF(1486, 4241, 9460, 10662, 16456, 27694)
	c.IntraTXType1[0][IntraD157] = makeCDF(439, 2838, 3522, 6737, 18058, 23754)
	c.IntraTXType1[0][IntraD203] = makeCDF(1190, 4233, 4855, 11670, 20281, 24377)
	c.IntraTXType1[0][IntraD67] = makeCDF(1045, 4312, 8647, 10159, 18644, 29335)
	c.IntraTXType1[0][IntraSmooth] = makeCDF(202, 3734, 4747, 7298, 17127, 24016)
	c.IntraTXType1[0][IntraSmoothV] = makeCDF(447, 4312, 6819, 8884, 16010, 23858)
	c.IntraTXType1[0][IntraSmoothH] = makeCDF(277, 4369, 5255, 8905, 16465, 22271)
	c.IntraTXType1[0][IntraPaeth] = makeCDF(3409, 5436, 10599, 15599, 19687, 24040)
	c.IntraTXType1[1][IntraDC] = makeCDF(1870, 13742, 14530, 16498, 23770, 27698)
	c.IntraTXType1[1][IntraVertical] = makeCDF(326, 8796, 14632, 15079, 19272, 27486)
	c.IntraTXType1[1][IntraHorizontal] = makeCDF(484, 7576, 7712, 14443, 19159, 22591)
	c.IntraTXType1[1][IntraD45] = makeCDF(1126, 15340, 15895, 17023, 20896, 30279)
	c.IntraTXType1[1][IntraD135] = makeCDF(655, 4854, 5249, 5913, 22099, 27138)
	c.IntraTXType1[1][IntraD113] = makeCDF(1299, 6458, 8885, 9290, 14851, 25497)
	c.IntraTXType1[1][IntraD157] = makeCDF(311, 5295, 5552, 6885, 16107, 22672)
	c.IntraTXType1[1][IntraD203] = makeCDF(883, 8059, 8270, 11258, 17289, 21549)
	c.IntraTXType1[1][IntraD67] = makeCDF(741, 7580, 9318, 10345, 16688, 29046)
	c.IntraTXType1[1][IntraSmooth] = makeCDF(110, 7406, 7915, 9195, 16041, 23329)
	c.IntraTXType1[1][IntraSmoothV] = makeCDF(363, 7974, 9357, 10673, 15629, 24474)
	c.IntraTXType1[1][IntraSmoothH] = makeCDF(153, 7647, 8112, 9936, 15307, 19996)
	c.IntraTXType1[1][IntraPaeth] = makeCDF(3511, 6332, 11165, 15335, 19323, 23594)

	// IntraTXType2: dav1d txtp_intra2 — reduced set or TX16x16
	// CDF4: 4 symbols (our nsymbs=5).
	// Groups 0 and 1 are uniform; group 2 (TX16x16) has context-specific values from dav1d.
	for m := 0; m < NumIntraModes; m++ {
		c.IntraTXType2[0][m] = makeCDF(6554, 13107, 19661, 26214)
		c.IntraTXType2[1][m] = makeCDF(6554, 13107, 19661, 26214)
	}
	// Group 2 from dav1d txtp_intra2[2]:
	c.IntraTXType2[2][IntraDC] = makeCDF(1127, 12814, 22772, 27483)
	c.IntraTXType2[2][IntraVertical] = makeCDF(145, 6761, 11980, 26667)
	c.IntraTXType2[2][IntraHorizontal] = makeCDF(362, 5887, 11678, 16725)
	c.IntraTXType2[2][IntraD45] = makeCDF(385, 15213, 18587, 30693)
	c.IntraTXType2[2][IntraD135] = makeCDF(25, 2914, 23134, 27903)
	c.IntraTXType2[2][IntraD113] = makeCDF(60, 4470, 11749, 23991)
	c.IntraTXType2[2][IntraD157] = makeCDF(37, 3332, 14511, 21448)
	c.IntraTXType2[2][IntraD203] = makeCDF(157, 6320, 13036, 17439)
	c.IntraTXType2[2][IntraD67] = makeCDF(119, 6719, 12906, 29396)
	c.IntraTXType2[2][IntraSmooth] = makeCDF(47, 5537, 12576, 21499)
	c.IntraTXType2[2][IntraSmoothV] = makeCDF(269, 6076, 11258, 23115)
	c.IntraTXType2[2][IntraSmoothH] = makeCDF(83, 5615, 12001, 17228)
	c.IntraTXType2[2][IntraPaeth] = makeCDF(1968, 5556, 12023, 18547)
}

// ---- Skip CDFs ----

func initSkipCDFs(c *CDFTables) {
	// From dav1d default_cdf.m.skip[3].
	c.Skip[0] = makeCDF(31671)
	c.Skip[1] = makeCDF(16515)
	c.Skip[2] = makeCDF(4576)
}

// ---- Coefficient CDFs (dav1d default_coef_cdf Q1 set) ----
// txSzSqrMap maps our TX size indices to the 5 square TX size contexts.
var txSzSqrMap = [13]int{0, 1, 2, 3, 4, 0, 0, 1, 1, 2, 2, 3, 3}

func initCoeffCDFs(c *CDFTables, baseQIdx int) {
	switch {
	case baseQIdx < 0:
		// Universal/uniform defaults (no Q selected).
		initCoeffCDFsUniform(c)
	case baseQIdx <= 20:
		initCoeffCDFsQ0(c)
	case baseQIdx <= 84:
		initCoeffCDFsQ1(c)
	case baseQIdx <= 170:
		initCoeffCDFsQ2(c)
	default:
		initCoeffCDFsQ3(c)
	}
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
	for txs := 0; txs < 5; txs++ {
		for pt := 0; pt < 2; pt++ {
			for ctx := 0; ctx < 9; ctx++ {
				c.EOBExtra[txs][pt][ctx] = makeCDF(16384)
			}
		}
	}
	for txs := 0; txs < 5; txs++ {
		for pt := 0; pt < 2; pt++ {
			for ec := 0; ec < 4; ec++ {
				c.CoeffBaseEOB[txs][pt][ec] = makeCDF(17837, 29055)
			}
		}
	}
	for txs := 0; txs < 5; txs++ {
		for pt := 0; pt < 2; pt++ {
			for ctx := 0; ctx < 41; ctx++ {
				c.CoeffBase[txs][pt][ctx] = makeCDF(12160, 23040, 28928)
			}
		}
	}
	for txs := 0; txs < 4; txs++ {
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
	c.FilterIntraMode = makeCDF(8949, 12776, 17211, 29558)
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
