task x {
  Int i
  command {
    echo ${i+1}
  }
  output {
    Int o = read_int(stdout())
  }
}

workflow w {
  call x
}
