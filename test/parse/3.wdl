task x {
  Int i

  command {
    echo ${i + 1} ${s} ${f + 3.2} ${join(i, ", ")} ${m["foo"]}
  }

  output {
    Int o = read_int(stdout())
  }
}

workflow w {
  call x
  call x as y
}
