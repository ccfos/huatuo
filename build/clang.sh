#!/bin/sh

usage() {
    echo "OVERVIEW: HuaTuo BPF compiler tool (clang LLVM)

USAGE: clang.sh -s <source.c> -o <output.o> -I [includes] -C '[compile_options]'
EXAMPLE:
    clang.sh -s example.bpf.c -o example.o            # run preprocess, compile, and assemble steps (-C '-c')
    clang.sh -s example.bpf.c -o example.o -I include -I include/4.18.0-193.6.3.el8_2.x86_64 # specify the headers, (-C '-c')
    clang.sh -s example.bpf.c -o example.o -C '-E'    # only run the preprocessor
    clang.sh -s example.bpf.c -o example.o -C '-S'    # only run preprocess and compilation steps"
}

SRC=
OBJ=
INCLUDES=
DEFAULT_INCLUDES="-I include -I include/4.18.0-193.6.3.el8_2.x86_64"
ARCH=$(uname --machine | sed 's/x86_64/x86/' | sed 's/aarch64/arm64/')
COMPILE_OPTIONS=
DEFAULT_COMPILE_OPTIONS="-Wall -O2 -g -target bpf -D__TARGET_ARCH_${ARCH} -mcpu=v1 -c"

while getopts 'hs:o:C:I:' opt
do
    case ${opt} in
        s)
            [ -n "${SRC}" ] && echo "-s(source) required 1 file (bpf.c)" && exit 1
            SRC=${OPTARG}
            ;;
        o)
            [ -n "${OBJ}" ] && echo "-o(output) required 1 file (output.o)" && exit 1
            OBJ=${OPTARG}
            ;;
        C)
            COMPILE_OPTIONS=${OPTARG}
            ;;
        I)
            INCLUDES="${INCLUDES} -I ${OPTARG}"
            ;;
        h)
            usage
            exit
            ;;
        ?)
            usage
            exit 1
            ;;
    esac
done

[ -z "${SRC}" ] && echo -e "-s must be specified, such as -c example.bpf.c \n\n $(usage)" && exit 1
[ -z "${OBJ}" ] && echo -e "-o must be specified, such as -o example.o \n\n $(usage)" && exit 1


# Note: parameter ${DEFAULT_COMPILE_OPTIONS} will be overwritten by ${COMPILE_OPTIONS} in ${OPTIONS}
OPTIONS="${DEFAULT_COMPILE_OPTIONS} ${COMPILE_OPTIONS}"
[ -z "${INCLUDES}" ] && INCLUDES="${DEFAULT_INCLUDES}"

clang ${OPTIONS} ${SRC} -o ${OBJ} ${INCLUDES}
