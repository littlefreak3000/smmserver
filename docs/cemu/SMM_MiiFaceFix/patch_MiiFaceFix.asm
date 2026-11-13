[SMM_MiiFaceFix]
moduleMatches = 0x64ABCBD7

; FFL "swap endianness of a float array" at 0x02B9F748 (r3 = array, r4 = count).
; The original loop sends every word through a float register (lfs/stfs), which alters NaN/denormal bit patterns.
; Replacement: for each word, load it byte-reversed and store it back, then return 0.
;   cmpwi r4,0 / blelr / mtctr r4 / loop: lwbrx r0,0,r3 / stw r0,0(r3) / addi r3,r3,4 / bdnz loop / li r3,0 / blr
.origin = 0x02B9F748
.int 0x2C040000, 0x4C810020, 0x7C8903A6, 0x7C001C2C, 0x90030000, 0x38630004, 0x4200FFF4, 0x38600000, 0x4E800020
