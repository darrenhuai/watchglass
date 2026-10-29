# Real photos used as test fixtures

Each file is a crop of a freely licensed photo, recompressed small. The
tests say which box on it they use and why.

| File | What it shows | Source | Author | Licence |
|---|---|---|---|---|
| clock-philips-106.jpg | An LED alarm clock showing 1:06, photographed at an angle. A 417x358 crop round the digits (pixels 505,325 to 922,683 of the 1280px wide version), saved with Pillow at JPEG quality 85. | [Wikimedia Commons](https://commons.wikimedia.org/wiki/File:LED_seven_segment_display_on_a_digital_clock_(16041065485).jpg) | cogdogblog | [CC BY 2.0](https://creativecommons.org/licenses/by/2.0/) |
| odo-vw.jpg | A car odometer showing 105616, its last two digits paler and set apart. A 377x128 crop (from pixel 509,786 of the 1280px wide version), JPEG quality 90. | [Wikimedia Commons](https://commons.wikimedia.org/wiki/File:VW_Odometer.jpg) | Sav127 | [CC BY-SA 3.0](https://creativecommons.org/licenses/by-sa/3.0/) |
| clock-seiko-wall.jpg | An LCD wall clock showing 1:16. A 454x313 crop (from pixel 496,138 of the 1280px wide version), JPEG quality 90. | [Wikimedia Commons](https://commons.wikimedia.org/wiki/File:LCD_digital_wall_clock_(Seiko).jpg) | Mk2010 | [CC BY-SA 3.0](https://creativecommons.org/licenses/by-sa/3.0/) |
| pump-us-834.jpg | A fuel pump's LCD showing 8.34, the housing along the top and right of the box. A 256x152 crop (from pixel 522,171 of the 1280px wide version), JPEG quality 90. | [Wikimedia Commons](https://commons.wikimedia.org/wiki/File:Gas_pump_display,_Jacksonville,_FL.jpg) | Anthony Inswasty | [CC BY-SA 4.0](https://creativecommons.org/licenses/by-sa/4.0/) |
| scale-kitchen-314.jpg | A kitchen scale showing 314 with a "g" over its last digit. A 261x154 crop (from pixel 575,994 of the 1280px wide version), JPEG quality 90. | [Wikimedia Commons](https://commons.wikimedia.org/wiki/File:2023_Elektroniczna_waga_kuchenna.jpg) | Jacek Halicki | [CC BY-SA 4.0](https://creativecommons.org/licenses/by-sa/4.0/) |
| meter-wb-0625.jpg | An electricity meter's LCD showing 06:25, its colon's dots a little offset. A 192x124 crop (from pixel 477,527 of the 1280px wide version), JPEG quality 90. | [Wikimedia Commons](https://commons.wikimedia.org/wiki/File:West_Bengal_State_Electricity_Distribution_Company_Limited_electricity_meter_%E2%80%93_Purulia,_West_Bengal,_India.jpg) | Xzantiyamoloy | [CC BY-SA 4.0](https://creativecommons.org/licenses/by-sa/4.0/) |
| meter-aclara.jpg | A smart meter's LCD showing 74590, the window frame on the right of the box. A 224x90 crop (from pixel 534,301 of the 1280px wide version), JPEG quality 90. | [Wikimedia Commons](https://commons.wikimedia.org/wiki/File:Aclara_I-210%2B_single_phase_smart_electricity_meter.jpg) | 277volts | [CC BY-SA 4.0](https://creativecommons.org/licenses/by-sa/4.0/) |
| meter-unit-496.jpg | A panel meter's LCD showing 4.96. A 244x161 crop (from pixel 694,181 of the 1280px wide version), JPEG quality 90. | [Wikimedia Commons](https://commons.wikimedia.org/wiki/File:Regulator_test.jpg) | tony_duell | [CC BY 2.0](https://creativecommons.org/licenses/by/2.0/) |

The crops are changes to the originals, and the CC BY-SA ones are shared
under the same licence as their originals.

With Binarize set to 128 and the box round the digits, the Philips photo
used to make the seven-segment decoder panic (see `sevenseg_crash_test.go`).
The others are where round 6 found the decoder missing digit cells (see
`sevenseg_cells_test.go`).
