# Symbol parser fixtures

These are synthetic, non-executable ELF data files authored for this test suite,
not binaries copied from another project. They contain no program headers or
machine code. All use little endian encoding and a leading null symbol.

The files freeze small outputs of the fixture builders from commit
`2da1330130c4e9815484dba68c347142c772d66d` (Go 1.24.6), allowing the builders
to be removed from the running tests. Do not run these files. Inspect headers
and symbols with `readelf -h -S -s FILE`; the minimal fixtures omit symbol
binding metadata (`sh_info`), so readelf may warn about local-symbol ordering.

| File | Layout and symbols |
| --- | --- |
| symbols32.elf | ELF32/i386; sections: null, strings, symtab. Strings: NUL + "target" + NUL. One function at 0x1000, size 0x10, name offset 1. |
| symbols64.elf | ELF64/x86-64; sections: null, strings, symtab, dynamic strings, dynsym. Static strings: NUL + "target" + NUL + "otherx" + NUL. Functions at 0x1000 and 0x1010, size 0x10, name offsets 1 and 8. Dynamic strings: NUL + "dynamic" + NUL; function at 0x1000, size 0x30. |
| compressed64.elf | ELF64; first string table is SHF_COMPRESSED/ZLIB. Expanded data: 4096 NUL bytes + "target" + NUL + "otherx" + NUL (4110 bytes). Two functions at 0x1000/0x1010, size 0x10, name offsets 4096/4103. Sections 3/4 retain a small auxiliary string/symbol table from the old fixture. |
| legacy64.elf | Same function data as compressed64.elf, but strings use the legacy ZLIB + big-endian size header. Section 3 contains NUL + ".zdebug_str" + NUL and is selected as shstrtab; section 1's name offset is 1. |

Tests load fresh bytes, then patch only the relevant name offset, symbol type,
address, or malformed header. No assembler, compiler, or compression tool is
required to run the tests. The fixed addresses are ELF-relative test values,
not host addresses.

For the legacy offset regression, the requested name offset is deliberately
**greater** than the compressed file size but less than the expanded size.
Using an offset below both sizes would not exercise that regression.
